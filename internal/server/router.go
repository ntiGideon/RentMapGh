// Package server wires middleware and routes.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/google/uuid"

	"rentmapgh/internal/config"
	"rentmapgh/internal/db"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/modules/auth"
	"rentmapgh/internal/modules/availability"
	"rentmapgh/internal/modules/listings"
	"rentmapgh/internal/modules/mandates"
	"rentmapgh/internal/modules/messages"
	"rentmapgh/internal/modules/notify"
	"rentmapgh/internal/modules/trust"
	"rentmapgh/internal/modules/users"
	"rentmapgh/internal/modules/verification"
	"rentmapgh/internal/modules/viewings"
	"rentmapgh/internal/modules/waitlist"
	"rentmapgh/internal/platform/sms"
	"rentmapgh/internal/platform/storage"
	"rentmapgh/internal/platform/video"
	mw "rentmapgh/internal/server/middleware"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/server/reqctx"
	"rentmapgh/web"
)

type Deps struct {
	Cfg    config.Config
	DB     *db.DB
	Assets *web.Assets
	SMS    sms.Sender
	Files  storage.Store // private uploads; evidence is sealed on top of it
	Media  storage.Store // public listing photos and videos
	// Listings is shared by the router and the video worker; New builds
	// one (without video) when it's nil.
	Listings *listings.Service
	// Hub carries realtime message events; main closes it on shutdown so
	// open event streams end. New makes one when it's nil.
	Hub *messages.Hub
}

// NewListings builds the listings service, with walk-through videos when
// ffmpeg is available.
func NewListings(d Deps) (*listings.Service, error) {
	svc := listings.NewService(d.DB.Ent, audit.New(d.DB.Ent), d.Cfg.LocationSecret, d.Media)
	if !d.Cfg.VideoEnabled {
		return svc, nil
	}
	tool, err := video.Find(d.Cfg.FFmpegPath, d.Cfg.FFprobePath)
	if err != nil {
		slog.Warn("walk-through videos off: ffmpeg/ffprobe not found")
		return svc, nil
	}
	if err := svc.EnableVideo(tool, d.Cfg.VideoInbox); err != nil {
		return nil, err
	}
	slog.Info("walk-through videos on", "ffmpeg", tool.FFmpeg, "inbox", d.Cfg.VideoInbox)
	return svc, nil
}

// evidenceStore wraps Files with the document key.
func evidenceStore(d Deps) *storage.Sealed {
	key, err := d.Cfg.DocumentKeyBytes()
	if err != nil {
		panic(err) // config.Load already validated it
	}
	sealed, err := storage.NewSealed(d.Files, key)
	if err != nil {
		panic(err)
	}
	return sealed
}

// newAvailability builds the freshness engine. Its links are signed with
// AUTH_SECRET (domain-separated).
func newAvailability(d Deps, v *viewings.Service) *availability.Service {
	return availability.NewService(d.DB.Ent, audit.New(d.DB.Ent), d.SMS, v, d.Cfg.AuthSecret, d.Cfg.BaseURL)
}

func newVerification(d Deps, log *audit.Log) *verification.Service {
	return verification.NewService(d.DB.Ent, evidenceStore(d), log, d.SMS, d.Cfg.BaseURL, d.Cfg.EvidenceRetention)
}

// StartJobs runs periodic maintenance until ctx ends. Safe on several
// replicas: every job is idempotent. (Moves to River with the job queue.)
func StartJobs(ctx context.Context, d Deps) {
	vs := newVerification(d, audit.New(d.DB.Ent))
	if d.Listings != nil {
		go d.Listings.RunVideoWorker(ctx)
	}
	notes := notify.NewService(d.DB.Ent, d.SMS, d.Hub) // Hub may be nil (tests): no live bell updates
	vs2 := viewings.NewService(d.DB.Ent, audit.New(d.DB.Ent), d.SMS, d.Cfg.BaseURL)
	vs2.SetNotifier(notes)
	avail := newAvailability(d, vs2)
	avail.SetNotifier(notes)
	trustSvc := trust.NewService(d.DB.Ent, vs2, mandates.NewService(d.DB.Ent, audit.New(d.DB.Ent), d.SMS, d.Cfg.AuthSecret, d.Cfg.BaseURL))
	// Hourly: expire unconfirmed listings and text "still available?" nudges
	// (daytime only), then refresh trust scores.
	every(ctx, 2*time.Minute, time.Hour, func() {
		if exp, nudged, err := avail.Sweep(ctx); err != nil {
			slog.ErrorContext(ctx, "jobs: availability", "err", err)
		} else if exp+nudged > 0 {
			slog.InfoContext(ctx, "jobs: availability", "expired", exp, "nudged", nudged)
		}
		if n, err := trustSvc.RefreshAll(ctx); err != nil {
			slog.ErrorContext(ctx, "jobs: trust", "err", err)
		} else if n > 0 {
			slog.InfoContext(ctx, "jobs: trust scores changed", "count", n)
		}
	})
	// Every 10 minutes: viewing reminders (24 h, 2 h) and feedback prompts.
	every(ctx, time.Minute, 10*time.Minute, func() {
		if n, err := vs2.Remind(ctx); err != nil {
			slog.ErrorContext(ctx, "jobs: reminders", "err", err)
		} else if n > 0 {
			slog.InfoContext(ctx, "jobs: reminders sent", "count", n)
		}
	})
	go func() {
		t := time.NewTimer(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := vs.PurgeEvidence(ctx); err != nil {
					slog.ErrorContext(ctx, "jobs: purge evidence", "err", err)
				} else if n > 0 {
					slog.InfoContext(ctx, "jobs: purged verification evidence", "count", n)
				}
				t.Reset(6 * time.Hour)
			}
		}
	}()
}

// every runs fn after first, then every interval, until ctx ends.
func every(ctx context.Context, first, interval time.Duration, fn func()) {
	go func() {
		t := time.NewTimer(first)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fn()
				t.Reset(interval)
			}
		}
	}()
}

func New(d Deps) http.Handler {
	r := chi.NewRouter()

	auditLog := audit.New(d.DB.Ent)
	otp := auth.NewOTP(d.DB.Ent, d.SMS, auditLog, d.Cfg.AuthSecret, d.Cfg.SMSDailyCap, hostOf(d.Cfg.BaseURL))
	authH := auth.NewHandler(d.DB.Ent, otp, auth.NewSessions(d.DB.Ent, d.Cfg.SessionTTL), auditLog, auth.HandlerConfig{
		Secret: d.Cfg.AuthSecret, Secure: d.Cfg.IsHTTPS(), DevInbox: devInbox(d.Cfg),
	})
	verifySvc := newVerification(d, auditLog)
	verifyH := verification.NewHandler(verifySvc)
	listingsSvc := d.Listings
	if listingsSvc == nil {
		listingsSvc = listings.NewService(d.DB.Ent, auditLog, d.Cfg.LocationSecret, d.Media)
	}
	mandatesSvc := mandates.NewService(d.DB.Ent, auditLog, d.SMS, d.Cfg.AuthSecret, d.Cfg.BaseURL)
	viewingsSvc := viewings.NewService(d.DB.Ent, auditLog, d.SMS, d.Cfg.BaseURL)
	hub := d.Hub
	if hub == nil {
		hub = messages.NewHub()
	}
	messagesSvc := messages.NewService(d.DB.Ent, auditLog, d.SMS, hub, d.Cfg.BaseURL)
	notifySvc := notify.NewService(d.DB.Ent, d.SMS, hub)
	viewingsSvc.SetNotifier(notifySvc)
	contact := func(ctx context.Context, id uuid.UUID) { listingsSvc.Bump(ctx, id, listings.StatContact) }
	viewingsSvc.OnContact(contact)
	messagesSvc.OnContact(contact)
	listingsH := listings.NewHandler(listingsSvc, mandatesSvc, listings.HandlerConfig{
		BaseURL: d.Cfg.BaseURL, Secret: d.Cfg.AuthSecret, Secure: d.Cfg.IsHTTPS(),
		Dashboard: func(ctx context.Context, lister uuid.UUID) (time.Duration, int) {
			d, n, _ := viewingsSvc.ResponseTime(ctx, lister)
			return d, n
		},
		Reliability: func(ctx context.Context, lister uuid.UUID) listings.Badges {
			r, err := viewingsSvc.ReliabilityOf(ctx, lister)
			if err != nil {
				return listings.Badges{}
			}
			return listings.Badges{RepliesFast: r.RepliesFast(), ShowsUp: r.ShowsUp(), Accurate: r.Accurate()}
		},
		OpenViewing: func(ctx context.Context, renter, listingID uuid.UUID) (string, string) {
			v, err := viewingsSvc.OpenFor(ctx, renter, listingID)
			if err != nil || v == nil {
				return "", ""
			}
			return "/viewings/" + v.ID.String(), viewings.When(v.StartsAt)
		}})
	cover := func(id uuid.UUID) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return listingsSvc.CoverURL(ctx, id)
	}
	viewingsH := viewings.NewHandler(viewingsSvc, cover)
	messagesH := messages.NewHandler(messagesSvc, cover, hub.Done())
	availSvc := newAvailability(d, viewingsSvc)
	availSvc.SetNotifier(notifySvc)
	availH := availability.NewHandler(availSvc)
	notifyH := notify.NewHandler(notifySvc)
	mandatesH := mandates.NewHandler(mandatesSvc)
	usersH := users.NewHandler(users.NewService(d.DB.Ent, auditLog, d.Files, verifySvc.PurgeUser), authH, verifySvc, auditLog)

	r.Use(chimw.RequestID)
	if d.Cfg.TrustProxy {
		r.Use(chimw.RealIP)
	}
	r.Use(mw.AccessLog)
	r.Use(mw.Recover(func(w http.ResponseWriter, r *http.Request) { render.Error(w, r, http.StatusInternalServerError) }))
	r.Use(mw.Security(d.Cfg.IsHTTPS()))
	r.Use(chimw.Compress(5, "text/html", "text/css", "text/javascript", "application/javascript", "image/svg+xml", "application/json", "text/plain"))
	r.Use(crossOrigin)
	r.Use(mw.DataSaver)
	r.Use(authH.LoadViewer)
	r.Use(navCounts(messagesSvc, viewingsSvc, notifySvc))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(reqctx.WithAssets(r.Context(), d.Assets)))
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) { render.Error(w, r, http.StatusNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) { render.Error(w, r, http.StatusMethodNotAllowed) })

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.DB.Ping(ctx); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	r.Handle(web.Prefix+"*", d.Assets.Handler())

	wl := waitlist.NewHandler(waitlist.NewService(d.DB.Ent), d.Cfg.BaseURL, d.Cfg.IsHTTPS())
	r.Get("/", wl.Home)
	r.With(rateLimit(5, time.Minute)).Post("/waitlist", wl.Join)

	// Phone OTP sign-in. Per-phone limits live in auth.OTP; these are per IP.
	r.Get("/login", authH.Login)
	r.With(rateLimit(10, time.Minute)).Post("/login", authH.SendCode)
	r.Get("/login/verify", authH.VerifyPage)
	r.With(rateLimit(20, time.Minute)).Post("/login/verify", authH.Verify)
	r.With(rateLimit(5, time.Minute)).Post("/login/resend", authH.Resend)
	r.Post("/logout", authH.Logout)
	r.Get("/u/{id}/avatar.jpg", usersH.Avatar)
	r.Get("/media/{id}/{file}", listingsH.Media)
	r.With(rateLimit(240, time.Minute)).Get("/search", listingsH.Search)
	r.With(rateLimit(240, time.Minute)).Get("/search/markers.geojson", listingsH.Markers)
	r.Get("/l/{id}/card", listingsH.Preview)
	r.With(rateLimit(120, time.Minute)).Get("/l/{id}/og.jpg", listingsH.OGImage)
	r.With(rateLimit(240, time.Minute)).Get("/places", listingsH.Places)
	r.With(rateLimit(60, time.Minute)).Post("/saved/{id}", listingsH.ToggleSaved)
	r.Get("/saved", listingsH.SavedPage)
	r.Get("/compare", listingsH.Compare)
	r.Get("/kumasi/{place}/{kind}", listingsH.AreaPage)
	r.Get("/sitemap.xml", listingsH.Sitemap)
	r.Get("/robots.txt", listingsH.Robots)
	// The lister's "still available?" link from SMS (no login: the signed link is the proof).
	r.With(rateLimit(30, time.Minute), noStore).Get("/c/{token}", availH.LinkPage)
	r.With(rateLimit(10, time.Minute), noStore).Post("/c/{token}", availH.LinkAnswer)
	r.Get("/l/{id}", listingsH.ListingPage)
	r.Get("/l/{id}/{slug}", listingsH.ListingPage)
	// A landlord's answer to an agent's request: the SMS link is the key.
	r.With(rateLimit(30, time.Minute), noStore).Get("/m/{token}", mandatesH.Offer)
	r.With(rateLimit(10, time.Minute), noStore).Post("/m/{token}", mandatesH.Decide)
	r.Get("/account/deleted", usersH.DeletedPage)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/onboarding", usersH.OnboardingPage)
		r.Post("/onboarding", usersH.Onboard)

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOnboarded)
			r.Use(rateLimit(60, time.Minute))
			r.Get("/account", usersH.Account)
			r.Post("/account/profile", usersH.UpdateProfile)
			r.Post("/account/roles", usersH.AddRole)
			r.Post("/account/sessions/{id}/revoke", usersH.RevokeSession)
			r.Post("/account/sessions/revoke-others", usersH.RevokeOthers)
			r.Post("/account/notifications", usersH.SaveNotifications)
			r.Post("/account/data-saver", usersH.DataSaver)
			r.Post("/account/avatar/remove", usersH.RemoveAvatar)
			r.With(auth.RequireRole("landlord")).Post("/account/landlord-profile", usersH.SaveLandlordProfile)
			r.With(auth.RequireRole("agent")).Post("/account/agent-profile", usersH.SaveAgentProfile)
			r.Get("/account/delete", usersH.DeletePage)
			r.Post("/account/delete", usersH.Delete)

			r.Get("/verify/identity", verifyH.IdentityPage)
			r.With(auth.RequireRole("agent")).Get("/verify/licence", verifyH.LicensePage)
		})
		// Uploads: a tighter per-IP limit than the rest of the account.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOnboarded)
			r.Use(rateLimit(10, time.Minute))
			r.Post("/account/avatar", usersH.UploadAvatar)
			r.Post("/verify/identity", verifyH.SubmitIdentity)
			r.With(auth.RequireRole("agent")).Post("/verify/licence", verifyH.SubmitLicense)
		})
	})

	// Listing wizard and "Your listings": landlords and agents.
	r.Route("/listings", func(r chi.Router) {
		r.Use(auth.RequireAuth, auth.RequireOnboarded, auth.RequireRole("landlord", "agent"), noStore)
		r.Get("/", listingsH.Mine)
		r.Get("/leads", listingsH.Leads)
		r.With(rateLimit(20, time.Hour)).Post("/new", listingsH.Create)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/edit/{step}", listingsH.Edit)
			r.Post("/edit/{step}", listingsH.Save)
			r.With(rateLimit(120, time.Minute)).Post("/edit/{step}/autosave", listingsH.Autosave)
			r.Post("/submit", listingsH.Submit)
			r.Post("/actions/{event}", listingsH.Action)
			r.With(rateLimit(30, time.Hour)).Post("/units", listingsH.AddUnit)
			r.With(rateLimit(240, time.Hour)).Post("/photos", listingsH.UploadPhotos)
			r.Post("/photos/order", listingsH.ReorderPhotos)
			r.Post("/photos/{mediaID}/{action}", listingsH.PhotoAction)
			r.Get("/stats", listingsH.Stats)
			r.Post("/price", listingsH.UpdatePrice)
			r.Get("/rented", availH.RentedPage)
			r.Post("/rented", availH.RentedAnswer)
			r.Get("/video", listingsH.VideoPanel)
			r.With(rateLimit(10, time.Hour)).Post("/video", listingsH.UploadVideo)
			r.With(rateLimit(20, time.Hour)).Post("/video/uploads", listingsH.StartVideoUpload)
			r.Get("/video/uploads/{uploadID}", listingsH.VideoUploadOffset)
			r.With(rateLimit(1200, time.Hour)).Post("/video/uploads/{uploadID}", listingsH.VideoChunk)
			r.Post("/video/delete", listingsH.DeleteVideo)
			r.With(auth.RequireRole("agent"), rateLimit(20, time.Hour)).Post("/owner", listingsH.OwnerAsk)
			r.With(auth.RequireRole("agent"), rateLimit(20, time.Hour)).Post("/owner/resend", listingsH.OwnerResend)
			r.With(auth.RequireRole("agent")).Post("/owner/cancel", listingsH.OwnerCancel)
		})
	})

	// Viewings: any signed-in, onboarded user; listers also set their hours.
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth, auth.RequireOnboarded, noStore)
		r.Get("/l/{id}/viewing", viewingsH.RequestPage)
		r.With(rateLimit(20, time.Hour)).Post("/l/{id}/viewing", viewingsH.Create)
		r.Get("/viewings", viewingsH.List)
		r.With(auth.RequireRole("landlord", "agent")).Get("/viewings/calendar", viewingsH.Week)
		r.With(auth.RequireRole("landlord", "agent")).Get("/viewings/hours", viewingsH.HoursPage)
		r.With(auth.RequireRole("landlord", "agent")).Post("/viewings/hours", viewingsH.SaveHours)
		r.Get("/viewings/{id}", viewingsH.Show)
		r.Get("/viewings/{id}/calendar.ics", viewingsH.Calendar)
		r.With(rateLimit(60, time.Minute)).Post("/viewings/{id}/{action}", viewingsH.Act)

		r.Get("/l/{id}/message", messagesH.NewPage)
		r.With(rateLimit(30, time.Hour)).Post("/l/{id}/message", messagesH.Start)
		r.Get("/messages", messagesH.Inbox)
		r.Get("/messages/{id}", messagesH.Thread)
		r.With(rateLimit(60, time.Minute)).Post("/messages/{id}", messagesH.Send)
		r.Get("/messages/{id}/since", messagesH.Since)
		r.With(rateLimit(20, time.Hour)).Post("/messages/{id}/report/{msgID}", messagesH.Report)
		r.Get("/events", messagesH.Events)
		r.With(rateLimit(10, time.Hour)).Post("/l/{id}/rented-report", availH.ReportRented)
		r.Get("/l/{id}/report", messagesH.ReportListingPage)
		r.With(rateLimit(10, time.Hour)).Post("/l/{id}/report", messagesH.ReportListing)
		r.Get("/viewings/{id}/feedback", viewingsH.FeedbackPage)
		r.With(rateLimit(20, time.Hour)).Post("/viewings/{id}/feedback", viewingsH.SaveFeedback)
		r.Get("/notifications", notifyH.Page)
	})

	// Back office: moderators and admins only.
	r.Route("/admin", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Use(auth.RequireRole("moderator", "admin"))
		r.Use(noStore)
		r.Use(adminCounts(verifySvc, listingsSvc, messagesSvc))
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/verifications", http.StatusSeeOther)
		})
		r.Get("/verifications", verifyH.AdminQueue)
		r.Get("/verifications/{id}", verifyH.AdminReview)
		r.Get("/verifications/{id}/files/{fileID}", verifyH.AdminFile)
		r.Post("/verifications/{id}/decision", verifyH.AdminDecide)
		r.Get("/listings", listingsH.AdminQueue)
		r.Get("/listings/{id}", listingsH.AdminReview)
		r.Post("/listings/{id}/decision", listingsH.AdminDecide)
		r.Get("/reports", messagesH.AdminReports)
		r.Get("/reports/{id}", messagesH.AdminReport)
		r.Post("/reports/{id}/decision", messagesH.AdminDecide)
	})

	return r
}

// adminCounts puts the queue badges in the context for the admin nav.
func adminCounts(v *verification.Service, l *listings.Service, m *messages.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := reqctx.AdminCounts{Listings: l.PendingCount(r.Context())}
			if rs, err := m.OpenReports(r.Context()); err == nil {
				c.Reports = len(rs)
			}
			if counts, err := v.Counts(r.Context()); err == nil {
				c.Verifications = counts["pending"]
			}
			next.ServeHTTP(w, r.WithContext(reqctx.WithAdminCounts(r.Context(), c)))
		})
	}
}

// navCounts puts the signed-in user's header badges (unread messages,
// viewings waiting for them) on the request. Anonymous requests skip it.
func navCounts(m *messages.Service, v *viewings.Service, n *notify.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			vw := reqctx.CurrentViewer(r.Context())
			if vw == nil || r.Header.Get("HX-Request") == "true" || strings.HasPrefix(r.URL.Path, "/static/") ||
				strings.HasPrefix(r.URL.Path, "/media/") || r.URL.Path == "/events" {
				next.ServeHTTP(w, r)
				return
			}
			var c reqctx.NavCounts
			c.Messages, _ = m.Unread(r.Context(), vw.UserID)
			c.Viewings, _ = v.Pending(r.Context(), vw.UserID)
			c.Notifications, _ = n.Unread(r.Context(), vw.UserID)
			next.ServeHTTP(w, r.WithContext(reqctx.WithNavCounts(r.Context(), c)))
		})
	}
}

// noStore keeps back-office pages out of every cache.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		next.ServeHTTP(w, r)
	})
}

// hostOf returns the bare host of the public URL (used in the WebOTP line).
func hostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// devInbox links the code step to Mailpit when SMS are delivered there.
func devInbox(cfg config.Config) string {
	if cfg.IsDev() && cfg.SMS.Provider == "mailpit" {
		return cfg.SMS.MailpitWebURL
	}
	return ""
}

// crossOrigin rejects cross-site state-changing requests (CSRF) using
// Sec-Fetch-Site / Origin. Being token-less keeps anonymous pages cacheable at
// the edge.
func crossOrigin(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		render.Error(w, r, http.StatusForbidden)
	}))
	return cop.Handler(next)
}

func rateLimit(n int, per time.Duration) func(http.Handler) http.Handler {
	return httprate.Limit(n, per,
		httprate.WithKeyFuncs(httprate.KeyByIP),
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			render.Error(w, r, http.StatusTooManyRequests)
		}),
	)
}

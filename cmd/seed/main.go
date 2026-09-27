// Command seed fills a development database with realistic live listings
// around KNUST, through the same wizard service real listers use (so every
// seeded listing passes validation, gets photos, terms and a fixed
// approximate point). Development only.
//
//	go run ./cmd/seed            # 60 listings
//	go run ./cmd/seed -n 200
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	"rentmapgh/internal/config"
	"rentmapgh/internal/db"
	"rentmapgh/internal/ent"
	"rentmapgh/internal/ent/agentmandate"
	"rentmapgh/internal/ent/roleassignment"
	"rentmapgh/internal/ent/user"
	"rentmapgh/internal/modules/audit"
	"rentmapgh/internal/modules/listings"
	"rentmapgh/internal/platform/geo"
	"rentmapgh/internal/platform/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

type lister struct {
	a     listings.Actor
	agent bool
}

func run() error {
	n := flag.Int("n", 60, "listings to create")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.IsDev() {
		return fmt.Errorf("refusing to seed APP_ENV=%s", cfg.Env)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		return err
	}
	defer d.Close()
	var media storage.Store = storage.Disk{Root: cfg.MediaDir}
	if cfg.MediaStore == "s3" {
		s3, err := storage.NewS3(cfg.S3)
		if err != nil {
			return err
		}
		if err := s3.EnsureBucket(ctx); err != nil {
			return err
		}
		media = s3
	}
	svc := listings.NewService(d.Ent, audit.New(d.Ent), cfg.LocationSecret, media)
	r := rand.New(rand.NewPCG(2026, 9))

	people := []struct {
		phone, name, role string
	}{
		{"+233200000101", "Akua Owusu", "landlord"},
		{"+233200000102", "Kofi Boateng", "landlord"},
		{"+233200000103", "Nana Yaa Asante", "landlord"},
		{"+233200000201", "Kwame Mensah", "agent"},
		{"+233200000202", "Efua Darko", "agent"},
	}
	var listers []lister
	for i, p := range people {
		u, err := upsertUser(ctx, d.Ent, p.phone, p.name, p.role, i%3 != 2)
		if err != nil {
			return err
		}
		listers = append(listers, lister{a: listings.Actor{UserID: u.ID, Roles: []string{p.role}, IdentityVerified: u.IdentityVerifiedAt != nil}, agent: p.role == "agent"})
	}

	near := geo.Neighbourhoods[:12] // the student belt and nearby
	made := 0
	for i := 0; i < *n; i++ {
		l := listers[r.IntN(len(listers))]
		nb := near[int(math.Abs(r.NormFloat64()*4))%len(near)]
		if err := one(ctx, svc, d.Ent, r, l, nb, i); err != nil {
			fmt.Fprintf(os.Stderr, "listing %d: %v\n", i, err)
			continue
		}
		made++
		fmt.Printf("\r%d/%d", made, *n)
	}
	fmt.Printf("\nseeded %d listings\n", made)
	return nil
}

func upsertUser(ctx context.Context, c *ent.Client, phone, name, role string, verified bool) (*ent.User, error) {
	u, err := c.User.Query().Where(user.Phone(phone)).Only(ctx)
	if ent.IsNotFound(err) {
		now := time.Now().UTC()
		cr := c.User.Create().SetPhone(phone).SetName(name).SetPhoneVerifiedAt(now).SetOnboardedAt(now)
		if verified {
			cr.SetIdentityVerifiedAt(now)
			if role == "agent" {
				cr.SetLicenseVerifiedAt(now)
			}
		}
		u, err = cr.Save(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("user %s: %w", phone, err)
	}
	has, err := c.RoleAssignment.Query().Where(roleassignment.UserID(u.ID), roleassignment.RoleEQ(roleassignment.Role(role))).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !has {
		if err := c.RoleAssignment.Create().SetUserID(u.ID).SetRole(roleassignment.Role(role)).Exec(ctx); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// kind is a template for one kind of place and its usual price.
type kind struct {
	unit, category, period string
	rent                   [2]int // GHS per period, min–max
	advance                [2]int // periods upfront
	sc                     string
	furnished, kitchen     string
	amenities              []string
	headline               []string
}

var kinds = []kind{
	{"single_room", "compound", "month", [2]int{250, 450}, [2]int{12, 24}, "no", "none", "shared",
		[]string{"water_storage", "ceiling_fan", "burglar_proof"}, []string{"Single room in a quiet compound", "Affordable single room near the main road"}},
	{"single_room_sc", "residential", "month", [2]int{450, 800}, [2]int{12, 12}, "yes", "none", "none",
		[]string{"gated", "water_storage", "tiled", "ceiling_fan"}, []string{"Self-contained single room", "Neat self-contained room with own washroom"}},
	{"chamber_hall", "compound", "month", [2]int{500, 900}, [2]int{12, 24}, "no", "none", "shared",
		[]string{"water_storage", "tiled", "porch"}, []string{"Chamber and hall in a family compound", "Spacious chamber and hall"}},
	{"chamber_hall_sc", "residential", "month", [2]int{900, 1600}, [2]int{6, 12}, "yes", "semi", "private",
		[]string{"gated", "watchman", "water_storage", "tiled", "wardrobe", "parking"}, []string{"Self-contained chamber & hall", "Modern chamber and hall, self-contained"}},
	{"hostel_1in1", "hostel", "academic_year", [2]int{5500, 9000}, [2]int{1, 1}, "yes", "full", "shared",
		[]string{"gated", "watchman", "cctv", "wifi", "study_room", "backup_power", "shuttle"}, []string{"1-in-a-room hostel, fully furnished", "Private hostel room near campus"}},
	{"hostel_2in1", "hostel", "academic_year", [2]int{3200, 5200}, [2]int{1, 1}, "yes", "full", "shared",
		[]string{"gated", "watchman", "wifi", "study_room", "common_kitchen"}, []string{"2-in-a-room hostel, 5 min to campus", "Shared hostel room with study area"}},
	{"hostel_4in1", "hostel", "academic_year", [2]int{2000, 3000}, [2]int{1, 1}, "no", "full", "shared",
		[]string{"gated", "watchman", "study_room", "common_kitchen"}, []string{"4-in-a-room hostel, budget friendly", "Budget hostel bed near the main gate"}},
	{"apartment_1bed", "residential", "month", [2]int{1500, 2800}, [2]int{6, 12}, "yes", "semi", "private",
		[]string{"gated", "watchman", "water_storage", "backup_power", "parking", "tiled", "wardrobe"}, []string{"1-bedroom apartment with parking", "Cosy 1-bedroom flat in a gated block"}},
	{"apartment_2bed", "residential", "month", [2]int{2500, 4500}, [2]int{6, 12}, "yes", "none", "private",
		[]string{"gated", "watchman", "cctv", "water_storage", "backup_power", "parking", "ac"}, []string{"2-bedroom apartment for a small family", "Bright 2-bedroom flat"}},
}

var landmarks = []string{"Behind the police station", "Near the Total filling station", "Opposite the SDA church",
	"Close to the Shoprite junction", "Two streets from the main road", "Next to the Methodist school", "Behind the lorry station"}

func one(ctx context.Context, svc *listings.Service, c *ent.Client, r *rand.Rand, l lister, nb geo.Neighbourhood, i int) error {
	k := kinds[r.IntN(len(kinds))]
	a := l.a
	dl, err := svc.CreateDraft(ctx, a)
	if err != nil {
		return err
	}
	id := dl.ID
	p := geo.Offset(nb.Centre, r.Float64()*700, r.Float64()*360) // within ~700 m of the centre
	rent := (k.rent[0] + r.IntN(k.rent[1]-k.rent[0]+1)) / 50 * 50
	adv := k.advance[0] + r.IntN(k.advance[1]-k.advance[0]+1)
	deposit, agentFee := 0, 0
	if l.agent {
		agentFee = rent / 2 / 50 * 50
	}
	if k.period == "month" && r.IntN(3) == 0 {
		deposit = rent
	}
	amen := k.amenities[:2+r.IntN(len(k.amenities)-1)]
	steps := []struct {
		step string
		form url.Values
	}{
		{"location", url.Values{"lat": {ff(p.Lat)}, "lng": {ff(p.Lng)}, "neighbourhood": {nb.Slug}, "landmark": {landmarks[r.IntN(len(landmarks))]}}},
		{"property", url.Values{"category": {k.category}, "name": {propertyName(r, k)}}},
		{"unit", url.Values{"unit_type": {k.unit}, "furnished": {k.furnished}, "self_contained": {k.sc}, "meter_type": {pick(r, "prepaid_own", "prepaid_shared")},
			"water_source": {pick(r, "gwcl", "borehole", "poly_tank", "mixed")}, "kitchen": {k.kitchen}, "bathrooms": {"1"}}},
		{"amenities", url.Values{"amenities": amen}},
		{"photos", url.Values{}},
		{"pricing", url.Values{"rent": {strconv.Itoa(rent)}, "rent_period": {k.period}, "advance_periods": {strconv.Itoa(adv)},
			"deposit": {strconv.Itoa(deposit)}, "agent_fee": {strconv.Itoa(agentFee)}, "service_charge": {strconv.Itoa(r.IntN(3) * 50)}, "viewing_fee": {"0"}}},
		{"details", url.Values{"headline": {k.headline[r.IntN(len(k.headline))] + " in " + nb.Label},
			"description": {"Clean, well-kept place in " + nb.Label + ". Water flows most days and the compound is quiet. Short walk to shops, the trotro stop and the main road. Viewing by appointment."}}},
	}
	for _, st := range steps {
		if st.step == "photos" {
			want, added := 3+r.IntN(7), 0
			for j := 0; added < want && j < 40; j++ {
				_, err := svc.AddPhoto(ctx, a, id, roomPhoto(uint64(i*100+j)))
				var verr listings.ValidationError
				switch {
				case errors.As(err, &verr): // too alike to one already added: draw another
				case err != nil:
					return fmt.Errorf("photo: %w", err)
				default:
					added++
				}
			}
		}
		if _, errs, err := svc.SaveStep(ctx, a, id, st.step, st.form, true); err != nil || len(errs) > 0 {
			return fmt.Errorf("%s: %v %v", st.step, err, errs)
		}
	}
	a.IdentityVerified = true // seeded listings go straight live
	if _, err := svc.Submit(ctx, a, id); err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	// Spread freshness over the last three weeks.
	confirmed := time.Now().UTC().Add(-time.Duration(r.IntN(21*24)) * time.Hour)
	if err := c.Listing.UpdateOneID(id).SetPublishedAt(confirmed.Add(-48 * time.Hour)).SetLastConfirmedAt(confirmed).Exec(ctx); err != nil {
		return err
	}
	if l.agent && r.IntN(2) == 0 {
		d, err := svc.Load(ctx, l.a, id)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		tok := uuid.New()
		return c.AgentMandate.Create().SetAgentID(a.UserID).SetPropertyID(d.P.ID).SetLandlordPhone("+233209990000").
			SetLandlordName("Mr Adjei").SetStatus(agentmandate.StatusApproved).SetTokenHash(tok[:]).
			SetSentAt(now).SetDecidedAt(now).SetValidUntil(now.AddDate(1, 0, 0)).Exec(ctx)
	}
	return nil
}

func propertyName(r *rand.Rand, k kind) string {
	if k.category == "hostel" {
		return pick(r, "Adom", "Nyame Bekyere", "Gye Nyame", "Sunrise", "Prestige", "Evandy", "Victory") + " Hostel"
	}
	if r.IntN(2) == 0 {
		return ""
	}
	return pick(r, "Owusu", "Asante", "Mensah", "Boateng", "Darko") + " " + pick(r, "Villa", "Court", "Residence", "House")
}

func pick(r *rand.Rand, opts ...string) string { return opts[r.IntN(len(opts))] }

func ff(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }

// roomPhoto draws a simple "room" (wall, floor, window, door, furniture) in
// a palette from seed; distinct seeds give distinct perceptual hashes.
func roomPhoto(seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	const w, h = 1200, 900
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	col := func(base [3]int, spread int) color.RGBA {
		c := func(v int) uint8 { return uint8(max(0, min(255, v+r.IntN(2*spread+1)-spread))) } //nolint:gosec // clamped to 0..255
		return color.RGBA{c(base[0]), c(base[1]), c(base[2]), 255}
	}
	walls := [][3]int{{236, 229, 214}, {214, 228, 236}, {232, 222, 200}, {220, 236, 222}, {240, 240, 236}}
	floors := [][3]int{{150, 110, 80}, {180, 170, 160}, {120, 120, 125}, {200, 190, 170}}
	horizon := h*55/100 + r.IntN(h/10)
	fill(img, image.Rect(0, 0, w, horizon), col(walls[r.IntN(len(walls))], 12))
	fill(img, image.Rect(0, horizon, w, h), col(floors[r.IntN(len(floors))], 15))
	wx := 80 + r.IntN(w/2)
	fill(img, image.Rect(wx, 120, wx+260+r.IntN(120), 320+r.IntN(80)), col([3]int{150, 200, 235}, 20))
	dx := w - 260 - r.IntN(200)
	fill(img, image.Rect(dx, horizon-420, dx+170, horizon), col([3]int{120, 80, 50}, 25))
	fx := 60 + r.IntN(w/3)
	fill(img, image.Rect(fx, horizon-60, fx+420+r.IntN(160), horizon+140), col([3]int{90 + r.IntN(120), 90 + r.IntN(120), 140}, 20))
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88})
	return buf.Bytes()
}

func fill(img *image.RGBA, rect image.Rectangle, c color.RGBA) {
	draw.Draw(img, rect, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

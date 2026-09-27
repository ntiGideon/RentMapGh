package listings

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"rentmapgh/internal/platform/ogimage"
	"rentmapgh/internal/platform/storage"
	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/pages"
)

// Share images: /l/{id}/og.jpg?v=<hash>. The hash covers everything drawn,
// so a price change gives a new URL (WhatsApp caches previews by URL) and
// each version is rendered once, then kept in the media store.

const ogVersion = "2" // bump when the design changes

func ogCard(v pages.ListingPageView) ogimage.Card {
	c := ogimage.Card{Price: v.Price, Per: v.Per, Title: v.UnitType + " · " + v.Area + ", " + v.City}
	if v.MoveInTotal != "" {
		c.MoveIn = v.MoveInTotal + " to move in · every fee listed"
	}
	return c
}

// ogHash identifies one rendering of a listing's card.
func ogHash(v pages.ListingPageView) string {
	c := ogCard(v)
	cover := ""
	if len(v.Photos) > 0 {
		cover = v.Photos[0].ID
	}
	sum := sha256.Sum256([]byte(ogVersion + "|" + cover + "|" + c.Price + "|" + c.Per + "|" + c.Title + "|" + c.MoveIn))
	return hex.EncodeToString(sum[:6])
}

// OGImagePath is the og:image path for a listing page.
func OGImagePath(v pages.ListingPageView) string { return "/l/" + v.ID + "/og.jpg?v=" + ogHash(v) }

// OGImage serves (rendering on first request) a listing's share image.
func (h *Handler) OGImage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, lister, err := h.svc.PublicListing(r.Context(), id, uuid.Nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v := publicView(d, lister, "", uuid.Nil, false, h.svc.now())
	hash := ogHash(v)
	key := "og/" + id.String() + "/" + hash + ".jpg"
	data, err := h.svc.media.Get(r.Context(), key)
	if errors.Is(err, storage.ErrNotFound) {
		data, err = h.renderOG(r, d, v)
		if err == nil {
			if perr := h.svc.media.Put(r.Context(), key, data); perr != nil {
				slog.WarnContext(r.Context(), "og image: store", "err", perr)
			}
		}
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "og image", "err", err, "listing", id)
		render.Error(w, r, http.StatusInternalServerError)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "image/jpeg")
	hd.Set("Content-Length", strconv.Itoa(len(data)))
	if r.URL.Query().Get("v") == hash {
		hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		hd.Set("Cache-Control", "public, max-age=3600") // old or missing version: the current one, briefly
	}
	_, _ = w.Write(data)
}

func (h *Handler) renderOG(r *http.Request, d *Item, v pages.ListingPageView) ([]byte, error) {
	var photo image.Image
	if cv := d.Cover(); cv != nil {
		for _, file := range []string{"w1600.jpg", "w800.jpg"} {
			raw, err := h.svc.media.Get(r.Context(), MediaKey(cv.ID, file))
			if err != nil {
				continue
			}
			if img, err := jpeg.Decode(bytes.NewReader(raw)); err == nil {
				photo = img
				break
			}
		}
	}
	return ogimage.Render(photo, ogCard(v))
}

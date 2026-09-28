package server

import (
	"net/http"

	"github.com/a-h/templ"

	"rentmapgh/internal/server/render"
	"rentmapgh/internal/views/layouts"
	"rentmapgh/internal/views/pages"
)

// docsUpdated is the "last updated" date on the help and legal pages;
// change it whenever their wording changes.
const docsUpdated = "28 September 2026"

// docPage serves one help or legal page.
func docPage(d Deps, path, title, desc string, page func(layouts.Meta, pages.DocView) templ.Component) http.HandlerFunc {
	m := layouts.Meta{Title: title, Description: desc, Path: path, URL: d.Cfg.BaseURL + path}
	v := pages.DocView{Contact: d.Cfg.SupportEmail, Updated: docsUpdated}
	return func(w http.ResponseWriter, r *http.Request) {
		render.Component(w, r, http.StatusOK, page(m, v))
	}
}

func mountDocs(r interface {
	Get(string, http.HandlerFunc)
}, d Deps) {
	r.Get("/how-we-verify", docPage(d, "/how-we-verify", "How we verify",
		"What RentMap's ID, licence and owner-confirmed badges mean and how we check them.", pages.HowWeVerify))
	r.Get("/safety", docPage(d, "/safety", "Safety tips",
		"How to rent safely in Ghana: never pay before a viewing, get receipts, and spot common scams.", pages.Safety))
	r.Get("/guidelines", docPage(d, "/guidelines", "Community guidelines",
		"The rules for listings, messages and viewings on RentMap.", pages.Guidelines))
	r.Get("/terms", docPage(d, "/terms", "Terms of use", "The terms for using RentMap Ghana.", pages.Terms))
	r.Get("/privacy", docPage(d, "/privacy", "Privacy policy",
		"What RentMap collects, why, who sees it, and your rights under Ghana's Data Protection Act.", pages.Privacy))
}

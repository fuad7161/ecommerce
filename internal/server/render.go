package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/justinas/nosurf"

	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/courier"
	"github.com/fuad71/ecommerce/internal/media"
	"github.com/fuad71/ecommerce/internal/money"
	"github.com/fuad71/ecommerce/internal/orders"
	"github.com/fuad71/ecommerce/web"
)

// Templates are compiled once at startup. Each page is parsed together with its
// layout and partials, giving one template set per page (key: "store/home").
type renderer struct {
	sets map[string]*template.Template
}

func newRenderer() (*renderer, error) {
	r := &renderer{sets: map[string]*template.Template{}}
	for _, area := range []string{"store", "admin"} {
		base := []string{"templates/" + area + "/layout.html"}
		partials, err := fs.Glob(web.FS, "templates/"+area+"/partials/*.html")
		if err != nil {
			return nil, err
		}
		base = append(base, partials...)
		pages, err := fs.Glob(web.FS, "templates/"+area+"/pages/*.html")
		if err != nil {
			return nil, err
		}
		for _, page := range pages {
			files := append(append([]string{}, base...), page)
			t, err := template.New(path.Base(page)).Funcs(funcs).ParseFS(web.FS, files...)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", page, err)
			}
			r.sets[area+"/"+strings.TrimSuffix(path.Base(page), ".html")] = t
		}
	}
	return r, nil
}

// render executes the named entry template ("layout" for full pages, or a block name
// for HTMX fragments) into a buffer first, so template errors never send half a page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page, entry string, data map[string]any) {
	t, ok := s.tmpl.sets[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("template %q not found", page))
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	s.baseData(r, data)
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, entry, data); err != nil {
		s.serverError(w, r, fmt.Errorf("render %s/%s: %w", page, entry, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) page(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	s.render(w, r, http.StatusOK, page, "layout", data)
}

func (s *Server) baseData(r *http.Request, data map[string]any) {
	st, err := s.settings.All(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "load settings", "err", err)
	}
	data["Store"] = st
	data["CSRF"] = nosurf.Token(r)
	data["Path"] = r.URL.Path
	data["Year"] = time.Now().Year()
	data["AppURL"] = s.cfg.AppURL
	if a := adminFrom(r); a != nil {
		data["Admin"] = a
	}
	if _, ok := data["Flash"]; !ok && strings.HasPrefix(r.URL.Path, "/admin") {
		data["Flash"] = s.popFlash(r)
	}
}

// popFlash returns and clears the admin flash message. scs panics when no session is
// loaded for the request (e.g. a 404 outside the session middleware), so guard it.
func (s *Server) popFlash(r *http.Request) (msg string) {
	defer func() { _ = recover() }()
	return s.sessions.PopString(r.Context(), "flash")
}

func (s *Server) flash(r *http.Request, msg string) {
	s.sessions.Put(r.Context(), "flash", msg)
}

var funcs = template.FuncMap{
	"money":       money.Format,
	"plainMoney":  money.Plain,
	"statusLabel": orders.StatusLabel,
	"img": func(key string) string {
		if key == "" {
			return ""
		}
		return "/media/" + key
	},
	"thumb": func(key string) string {
		if key == "" {
			return ""
		}
		return "/media/" + media.ThumbKey(key)
	},
	"date": func(t time.Time) string { return t.In(orders.Dhaka()).Format("02 Jan 2006, 3:04 PM") },
	"day":  func(t time.Time) string { return t.In(orders.Dhaka()).Format("02 Jan") },
	"dateInput": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02")
	},
	"add": func(a, b int) int { return a + b },
	"pct": func(part, whole any) int {
		p, w := toInt64(part), toInt64(whole)
		if w <= 0 {
			return 0
		}
		return int(p * 100 / w)
	},
	"seq": func(from, to int) []int {
		var out []int
		for i := from; i <= to; i++ {
			out = append(out, i)
		}
		return out
	},
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[fmt.Sprint(kv[i])] = kv[i+1]
		}
		return m
	},
	"json": func(v any) (template.JS, error) {
		b, err := json.Marshal(v)
		return template.JS(b), err
	},
	"can": func(a *auth.Admin, perm string) bool { return a.Can(auth.Permission(perm)) },
	// pageURL rewrites the "page" query parameter of the current query string.
	"pageURL": func(base string, q url.Values, page int) string {
		v := url.Values{}
		for k, vals := range q {
			v[k] = vals
		}
		v.Set("page", strconv.Itoa(page))
		return base + "?" + v.Encode()
	},
	"deref": func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	},
	"firstLetter": func(s string) string {
		for _, r := range s {
			return strings.ToUpper(string(r))
		}
		return "?"
	},
	"truncate": func(s string, n int) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return string(r[:n]) + "…"
	},
	"statusColor": func(s string) string {
		switch s {
		case orders.StatusPending:
			return "bg-amber-100 text-amber-800"
		case orders.StatusConfirmed, orders.StatusProcessing, orders.StatusReadyToShip:
			return "bg-sky-100 text-sky-800"
		case orders.StatusShipped, orders.StatusInTransit:
			return "bg-indigo-100 text-indigo-800"
		case orders.StatusDelivered:
			return "bg-emerald-100 text-emerald-800"
		case orders.StatusCancelled, orders.StatusReturned:
			return "bg-rose-100 text-rose-800"
		case "active":
			return "bg-emerald-100 text-emerald-800"
		case "draft":
			return "bg-slate-100 text-slate-700"
		case "inactive", "archived":
			return "bg-rose-100 text-rose-800"
		}
		return "bg-slate-100 text-slate-700"
	},
	"orderStatuses": func() []string { return orders.Statuses },
	"trackingSteps": func() []string { return orders.TrackingSteps },
	"lower":         strings.ToLower,
	"courierLabel":  courier.Label,
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	}
	return 0
}

package httpapi

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"slices"
	"strings"
)

//go:embed openapi.json
var openAPIJSON []byte

// docsTmpl renders the spec as a page. Parsed at init like the dashboard, so a
// broken template or an unparseable spec fails the binary rather than a request.
var docsTmpl = template.Must(template.New("docs.html").ParseFS(uiFS, "templates/docs.html"))

// apiSpec is the embedded specification, decoded once for the docs page.
var apiSpec = mustDecodeSpec(openAPIJSON)

// --- the slice of OpenAPI the docs page renders ---

type specDoc struct {
	Info struct {
		Title       string `json:"title"`
		Version     string `json:"version"`
		Description string `json:"description"`
	} `json:"info"`
	Tags []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"tags"`
	Paths      map[string]map[string]specOp `json:"paths"`
	Components struct {
		Schemas map[string]specSchema `json:"schemas"`
	} `json:"components"`
}

type specOp struct {
	Tags        []string            `json:"tags"`
	Summary     string              `json:"summary"`
	Description string              `json:"description"`
	Security    []map[string]any    `json:"security"`
	Parameters  []specParam         `json:"parameters"`
	RequestBody *specBody           `json:"requestBody"`
	Responses   map[string]specResp `json:"responses"`
}

type specParam struct {
	Name        string   `json:"name"`
	In          string   `json:"in"`
	Required    bool     `json:"required"`
	Description string   `json:"description"`
	Schema      specProp `json:"schema"`
}

type specBody struct {
	Required bool                    `json:"required"`
	Content  map[string]specMediaObj `json:"content"`
}

type specMediaObj struct {
	Schema specProp `json:"schema"`
}

type specResp struct {
	Description string                  `json:"description"`
	Content     map[string]specMediaObj `json:"content"`
}

type specSchema struct {
	Description string              `json:"description"`
	Required    []string            `json:"required"`
	Properties  map[string]specProp `json:"properties"`
}

type specProp struct {
	Type        string    `json:"type"`
	Format      string    `json:"format"`
	Description string    `json:"description"`
	Enum        []string  `json:"enum"`
	Ref         string    `json:"$ref"`
	Items       *specProp `json:"items"`
}

// name renders a property's type for display, resolving $ref to the bare schema
// name and unwrapping arrays.
func (p specProp) name() string {
	switch {
	case p.Ref != "":
		return refName(p.Ref)
	case p.Type == "array" && p.Items != nil:
		return p.Items.name() + "[]"
	case len(p.Enum) > 0:
		return strings.Join(p.Enum, " | ")
	case p.Format != "":
		return p.Type + " (" + p.Format + ")"
	case p.Type == "":
		return "object"
	}
	return p.Type
}

func refName(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

func mustDecodeSpec(raw []byte) specDoc {
	var s specDoc
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&s); err != nil {
		panic(fmt.Sprintf("httpapi: openapi.json is not valid JSON: %v", err))
	}
	return s
}

// --- view model ---

type docsData struct {
	Title       string
	Version     string
	Description string
	Groups      []docsGroup
	Schemas     []docsSchema
}

type docsGroup struct {
	Name        string
	Description string
	Ops         []docsOp
}

type docsOp struct {
	Method      string
	Path        string
	Summary     string
	Description string
	Auth        bool
	Params      []specParam
	Body        string // request body schema name, empty when there is none
	Responses   []docsResp
}

type docsResp struct {
	Code        string
	Description string
	Schema      string
}

type docsSchema struct {
	Name        string
	Description string
	Props       []docsProp
}

type docsProp struct {
	Name        string
	Type        string
	Required    bool
	Description string
}

// methodOrder is the order operations appear within a group.
var methodOrder = map[string]int{"get": 0, "post": 1, "put": 2, "patch": 3, "delete": 4}

// buildDocs turns the spec into the page's view model: operations grouped by
// tag in the order the spec declares its tags, schemas alphabetically.
func buildDocs(s specDoc) docsData {
	d := docsData{Title: s.Info.Title, Version: s.Info.Version, Description: s.Info.Description}

	byTag := map[string][]docsOp{}
	for path, ops := range s.Paths {
		for method, op := range ops {
			o := docsOp{
				Method:      strings.ToUpper(method),
				Path:        path,
				Summary:     op.Summary,
				Description: op.Description,
				Auth:        len(op.Security) > 0,
				Params:      op.Parameters,
				Responses:   responses(op.Responses),
			}
			if op.RequestBody != nil {
				if m, ok := op.RequestBody.Content["application/json"]; ok {
					o.Body = m.Schema.name()
				}
			}
			tag := "other"
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			byTag[tag] = append(byTag[tag], o)
		}
	}

	for _, t := range s.Tags {
		ops := byTag[t.Name]
		if len(ops) == 0 {
			continue
		}
		slices.SortFunc(ops, func(a, b docsOp) int {
			if c := cmp.Compare(a.Path, b.Path); c != 0 {
				return c
			}
			return cmp.Compare(methodOrder[strings.ToLower(a.Method)], methodOrder[strings.ToLower(b.Method)])
		})
		d.Groups = append(d.Groups, docsGroup{Name: t.Name, Description: t.Description, Ops: ops})
	}

	for _, name := range slices.Sorted(maps.Keys(s.Components.Schemas)) {
		sc := s.Components.Schemas[name]
		ds := docsSchema{Name: name, Description: sc.Description}
		for _, prop := range slices.Sorted(maps.Keys(sc.Properties)) {
			p := sc.Properties[prop]
			ds.Props = append(ds.Props, docsProp{
				Name:        prop,
				Type:        p.name(),
				Required:    slices.Contains(sc.Required, prop),
				Description: p.Description,
			})
		}
		d.Schemas = append(d.Schemas, ds)
	}
	return d
}

// responses flattens a response map into a list ordered by status code.
func responses(m map[string]specResp) []docsResp {
	out := make([]docsResp, 0, len(m))
	for _, code := range slices.Sorted(maps.Keys(m)) {
		r := m[code]
		d := docsResp{Code: code, Description: r.Description}
		for _, media := range []string{"application/json", "text/html", "text/plain"} {
			if mo, ok := r.Content[media]; ok {
				if n := mo.Schema.name(); n != "" && n != "string" && n != "object" {
					d.Schema = n
				}
				break
			}
		}
		out = append(out, d)
	}
	return out
}

// --- handlers ---

// handleOpenAPI serves the specification verbatim.
func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPIJSON)
}

// handleDocs renders the specification as a page, so the two cannot disagree.
func (s *Server) handleDocs(w http.ResponseWriter, _ *http.Request) {
	var buf bytes.Buffer
	if err := docsTmpl.Execute(&buf, buildDocs(apiSpec)); err != nil {
		s.log.Error("docs render failed", "err", err)
		http.Error(w, "kudzu: cannot render docs", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

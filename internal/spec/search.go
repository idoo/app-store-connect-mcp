package spec

import (
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Field weights for the BM25-lite term frequency.
const (
	weightResource = 3.0
	weightID       = 2.0
	weightOther    = 1.0
	bm25K          = 1.2
	bm25B          = 0.75
	defaultLimit   = 10
	maxLimit       = 50
)

// synonyms maps query words to the vocabulary of operation ids.
var synonyms = map[string]string{
	"list": "collection", "all": "collection",
	"show": "get", "read": "get", "fetch": "get",
	"add": "create", "new": "create", "make": "create",
	"edit": "update", "change": "update", "modify": "update",
	"remove": "delete",
}

type searchDoc struct {
	tf  map[string]float64
	len float64
}

type searchIndex struct {
	docs   []searchDoc
	df     map[string]int
	avgLen float64
}

func buildSearch(ops []Op) searchIndex {
	s := searchIndex{docs: make([]searchDoc, len(ops)), df: map[string]int{}}
	var total float64
	for i := range ops {
		d := searchDoc{tf: map[string]float64{}}
		add := func(w float64, toks []string) {
			for _, t := range toks {
				d.tf[t] += w
				d.len += w
			}
		}
		parts := strings.Split(ops[i].ID, "_")
		add(weightResource, []string{stem(strings.ToLower(parts[0]))})
		for _, p := range parts {
			add(weightID, tokens(p))
		}
		for _, seg := range splitPath(ops[i].Path) {
			if !isParam(seg) {
				add(weightOther, tokens(seg))
			}
		}
		for _, t := range ops[i].Tags {
			add(weightOther, tokens(t))
		}
		add(weightOther, tokens(ops[i].Summary))
		for t := range d.tf {
			s.df[t]++
		}
		total += d.len
		s.docs[i] = d
	}
	if len(ops) > 0 {
		s.avgLen = total / float64(len(ops))
	}
	return s
}

// Search ranks operations for a free-text query (keywords, tags, resource
// types or an operation id). It implements [tool.Spec].
func (x *Index) Search(query string, limit int) []tool.SearchHit {
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil
	}
	compounds := resourceCompounds(query)
	readQuery := !slices.ContainsFunc(terms, func(t string) bool { return t == "create" || t == "update" || t == "delete" })
	n := float64(len(x.search.docs))
	var hits []tool.SearchHit
	for i, d := range x.search.docs {
		var score float64
		for _, t := range terms {
			tf := d.tf[t]
			if tf == 0 {
				continue
			}
			df := float64(x.search.df[t])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			score += idf * tf * (bm25K + 1) / (tf + bm25K*(1-bm25B+bm25B*d.len/x.search.avgLen))
		}
		op := &x.file.Ops[i]
		if score > 0 {
			score *= boost(op, compounds, readQuery)
		}
		if strings.EqualFold(strings.TrimSpace(query), op.ID) {
			score += 1000
		}
		if score > 0 {
			hits = append(hits, tool.SearchHit{Operation: toOperation(op), Score: math.Round(score*1000) / 1000})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		return hits[a].ID < hits[b].ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Ranking multipliers applied on top of BM25.
const (
	boostResource   = 1.5 // the operation returns exactly the resource the query names
	boostRead       = 1.1 // GET when the query has no write verb
	boostCollection = 1.1 // list operations when the query has no write verb
)

// boost favours operations whose target resource is named by the query
// ("app events" → appEvents_getInstance, apps_appEvents_getToManyRelated)
// and, for read queries, GETs and collections.
func boost(op *Op, compounds map[string]bool, readQuery bool) float64 {
	b := 1.0
	parts := strings.Split(op.ID, "_")
	if compounds[stem(strings.ToLower(targetType(op.ID)))] {
		b *= boostResource
	}
	if readQuery && op.Method == "GET" {
		b *= boostRead
		if v := parts[len(parts)-1]; v == "getCollection" || v == "getToManyRelated" {
			b *= boostCollection
		}
	}
	return b
}

// resourceCompounds returns every run of consecutive query words joined,
// lowercased and stemmed ("app events" → appevent, app, event), to compare
// with stemmed resource names.
func resourceCompounds(q string) map[string]bool {
	words := strings.FieldsFunc(strings.ToLower(q), notAlnum)
	out := map[string]bool{}
	for i := range words {
		for j := i + 1; j <= len(words) && j-i <= 4; j++ {
			out[stem(strings.Join(words[i:j], ""))] = true
		}
	}
	return out
}

// queryTerms tokenizes a query, adding whole-word compounds and synonyms.
func queryTerms(q string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, w := range strings.FieldsFunc(q, notAlnum) {
		lw := strings.ToLower(w)
		if syn, ok := synonyms[lw]; ok {
			add(syn)
			continue
		}
		add(stem(lw))
		for _, t := range tokens(w) {
			add(t)
		}
	}
	return out
}

// tokens splits s on non-alphanumerics and camelCase boundaries, lowercases
// and stems each word. A camelCase compound also yields its whole form.
func tokens(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(s, notAlnum) {
		parts := camel(w)
		if len(parts) > 1 {
			out = append(out, stem(strings.ToLower(w)))
		}
		for _, p := range parts {
			out = append(out, stem(strings.ToLower(p)))
		}
	}
	return out
}

func notAlnum(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// camel splits "appStoreVersions" into [app Store Versions] and keeps
// acronym runs ("IAPs" → [IAPs]) together.
func camel(w string) []string {
	var parts []string
	rs := []rune(w)
	start := 0
	for i := 1; i < len(rs); i++ {
		if unicode.IsUpper(rs[i]) && !unicode.IsUpper(rs[i-1]) {
			parts = append(parts, string(rs[start:i]))
			start = i
		}
	}
	return append(parts, string(rs[start:]))
}

// stem applies a tiny English plural reduction.
func stem(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	}
	return w
}

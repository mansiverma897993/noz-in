package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mansiverma897993/signoz/internal/target/signoz"
)

const targetKindNone = "none"

// emittedQueryIdentity binds a report record to the static query specification
// persisted in the generated dashboard. Runtime window and variable values are
// deliberately excluded; query kind, name, expression, panel/query mode, and
// the exact emitted query body are included.
type emittedQueryIdentity struct {
	TargetKind       string
	TargetQueryName  string
	TargetExpression string
	SHA256           string
}

type canonicalEmittedQuerySpec struct {
	TargetKind       string `json:"targetKind"`
	TargetQueryName  string `json:"targetQueryName"`
	TargetExpression string `json:"targetExpression"`
	Body             any    `json:"body"`
}

type canonicalEmittedQueryBody struct {
	SchemaVersion  string                `json:"schemaVersion"`
	RequestType    string                `json:"requestType"`
	CompositeQuery signoz.CompositeQuery `json:"compositeQuery"`
	FormatOptions  *signoz.FormatOptions `json:"formatOptions,omitempty"`
	NoCache        bool                  `json:"noCache"`
}

type canonicalNonEmittedBody struct {
	Emitted bool `json:"emitted"`
}

func emittedQuerySpec(widget signoz.Widget, refID string) (emittedQueryIdentity, bool, error) {
	name := defaultEmittedQueryName(refID)
	request, err := signoz.DashboardRequestForWidgetWindow(widget, nil, time.Unix(3600, 0), time.Hour)
	if err != nil {
		return emittedQueryIdentity{}, false, fmt.Errorf("construct static target query envelope: %w", err)
	}
	identity, found, err := emittedQuerySpecFromRequest(request, name)
	if err != nil {
		return emittedQueryIdentity{}, false, fmt.Errorf("inspect widget %q query envelope: %w", widget.Title, err)
	}
	return identity, found, nil
}

func emittedQuerySpecFromRequest(request signoz.QueryRangeRequest, name string) (emittedQueryIdentity, bool, error) {
	body := canonicalEmittedQueryBody{
		SchemaVersion:  request.SchemaVersion,
		RequestType:    request.RequestType,
		CompositeQuery: request.CompositeQuery,
		FormatOptions:  request.FormatOptions,
		NoCache:        request.NoCache,
	}
	var matches []canonicalEmittedQuerySpec
	for _, envelope := range request.CompositeQuery.Queries {
		var queryName string
		var expression string
		switch spec := envelope.Spec.(type) {
		case signoz.PromQLSpec:
			if envelope.Type != "promql" {
				return emittedQueryIdentity{}, false, fmt.Errorf("target envelope type %q contains a PromQL specification", envelope.Type)
			}
			queryName = spec.Name
			expression = spec.Query
		case signoz.BuilderQuerySpec:
			if envelope.Type != "builder_query" {
				return emittedQueryIdentity{}, false, fmt.Errorf("target envelope type %q contains a Builder query specification", envelope.Type)
			}
			queryName = spec.Name
			encoded, encodeErr := json.Marshal(spec)
			if encodeErr != nil {
				return emittedQueryIdentity{}, false, fmt.Errorf("encode emitted Builder query %q: %w", name, encodeErr)
			}
			expression = string(encoded)
		case signoz.FormulaSpec:
			if envelope.Type != "builder_formula" {
				return emittedQueryIdentity{}, false, fmt.Errorf("target envelope type %q contains a Builder formula specification", envelope.Type)
			}
			queryName = spec.Name
			expression = spec.Expression
		default:
			return emittedQueryIdentity{}, false, fmt.Errorf("target envelope type %q contains unsupported specification %T", envelope.Type, envelope.Spec)
		}
		if queryName != name {
			continue
		}
		matches = append(matches, canonicalEmittedQuerySpec{
			TargetKind:       envelope.Type,
			TargetQueryName:  queryName,
			TargetExpression: expression,
			Body:             body,
		})
	}

	if len(matches) == 0 {
		return emittedQueryIdentity{}, false, nil
	}
	if len(matches) != 1 {
		return emittedQueryIdentity{}, false, fmt.Errorf("target request contains %d emitted queries named %q", len(matches), name)
	}
	identity, err := hashEmittedQuerySpec(matches[0])
	return identity, true, err
}

type targetArtifactWire struct {
	SchemaVersion  string                         `json:"schemaVersion"`
	Start          uint64                         `json:"start"`
	End            uint64                         `json:"end"`
	RequestType    string                         `json:"requestType"`
	CompositeQuery targetArtifactCompositeQuery   `json:"compositeQuery"`
	FormatOptions  *signoz.FormatOptions          `json:"formatOptions,omitempty"`
	Variables      map[string]signoz.VariableItem `json:"variables,omitempty"`
	NoCache        bool                           `json:"noCache,omitempty"`
}

type targetArtifactCompositeQuery struct {
	Queries []targetArtifactQueryEnvelope `json:"queries"`
}

type targetArtifactQueryEnvelope struct {
	Type string          `json:"type"`
	Spec json.RawMessage `json:"spec"`
}

func decodeTargetArtifact(data json.RawMessage) (signoz.QueryRangeRequest, error) {
	var wire targetArtifactWire
	if err := decodeStrictJSON(data, &wire); err != nil {
		return signoz.QueryRangeRequest{}, err
	}
	request := signoz.QueryRangeRequest{
		SchemaVersion: wire.SchemaVersion,
		Start:         wire.Start,
		End:           wire.End,
		RequestType:   wire.RequestType,
		FormatOptions: wire.FormatOptions,
		Variables:     wire.Variables,
		NoCache:       wire.NoCache,
	}
	request.CompositeQuery.Queries = make([]signoz.QueryEnvelope, 0, len(wire.CompositeQuery.Queries))
	for index, envelope := range wire.CompositeQuery.Queries {
		var spec any
		switch envelope.Type {
		case "promql":
			var value signoz.PromQLSpec
			if err := decodeStrictJSON(envelope.Spec, &value); err != nil {
				return signoz.QueryRangeRequest{}, fmt.Errorf("decode PromQL spec %d: %w", index, err)
			}
			spec = value
		case "builder_query":
			var value signoz.BuilderQuerySpec
			if err := decodeStrictJSON(envelope.Spec, &value); err != nil {
				return signoz.QueryRangeRequest{}, fmt.Errorf("decode Builder query spec %d: %w", index, err)
			}
			spec = value
		case "builder_formula":
			var value signoz.FormulaSpec
			if err := decodeStrictJSON(envelope.Spec, &value); err != nil {
				return signoz.QueryRangeRequest{}, fmt.Errorf("decode Builder formula spec %d: %w", index, err)
			}
			spec = value
		default:
			return signoz.QueryRangeRequest{}, fmt.Errorf("target artifact contains unsupported query type %q", envelope.Type)
		}
		request.CompositeQuery.Queries = append(request.CompositeQuery.Queries, signoz.QueryEnvelope{Type: envelope.Type, Spec: spec})
	}
	return request, nil
}

func decodeStrictJSON(data []byte, destination any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("JSON value is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON contains multiple values")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

func nonEmittedQuerySpec(refID string) (emittedQueryIdentity, error) {
	name := defaultEmittedQueryName(refID)
	return hashEmittedQuerySpec(canonicalEmittedQuerySpec{
		TargetKind:       targetKindNone,
		TargetQueryName:  name,
		TargetExpression: "",
		Body:             canonicalNonEmittedBody{Emitted: false},
	})
}

func hashEmittedQuerySpec(spec canonicalEmittedQuerySpec) (emittedQueryIdentity, error) {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return emittedQueryIdentity{}, fmt.Errorf("encode canonical emitted query specification: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return emittedQueryIdentity{
		TargetKind:       spec.TargetKind,
		TargetQueryName:  spec.TargetQueryName,
		TargetExpression: spec.TargetExpression,
		SHA256:           fmt.Sprintf("%x", digest[:]),
	}, nil
}

func defaultEmittedQueryName(refID string) string {
	name := strings.TrimSpace(refID)
	if name == "" {
		return "A"
	}
	return name
}

func targetKindForEmittedKind(kind string) (string, bool) {
	switch kind {
	case "builder":
		return "builder_query", true
	case "formula":
		return "builder_formula", true
	case "promql":
		return "promql", true
	case "none":
		return targetKindNone, true
	default:
		return "", false
	}
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

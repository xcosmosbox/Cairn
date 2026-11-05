package semanticevidence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type v012SourceOnlyClient struct {
	t        *testing.T
	source   sourceFixture
	rows     []dktypes.AnnotatedItem
	proof    []map[string]any
	requests []llm.CompleteRequest
}

func (c *v012SourceOnlyClient) ProviderName() string { return "source-only-annotation-fixture" }
func (c *v012SourceOnlyClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.requests = append(c.requests, req)
	if !strings.Contains(req.User, c.source.Content) {
		c.t.Fatal("authoritative source absent from annotation/assessment input")
	}
	for _, forbidden := range []string{"\"cases\"", "\"expected\"", "\"source_id\"", "\"owner\"", "original_bytes", "expected_inventory_ref"} {
		if strings.Contains(req.User, forbidden) {
			c.t.Fatalf("acceptance material entered model prompt: %s", forbidden)
		}
	}
	if len(c.requests) == 1 {
		b, _ := json.Marshal(map[string]any{"items": c.rows})
		return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}, nil
	}
	if len(c.requests) != 2 {
		c.t.Fatal("accepted sibling was unexpectedly rerun")
	}
	b, _ := json.Marshal(map[string]any{"supported": true, "reason": "explicit authored source carriers retained", "unsupported_items": []int{}, "omissions": []string{}, "subject_coverage": c.proof})
	return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}, nil
}

// Source-specific fixture semantics, hand-authored for the immutable eight
// originals. This is scripted plumbing evidence, not a model's semantic gold.
func supplementSubjectRows(id string) ([]dktypes.AnnotatedItem, []map[string]any) {
	type authored struct {
		name, detail, quote, subject string
		same, preserves              bool
	}
	var material []authored
	switch id {
	case "F01":
		material = []authored{{"Atlas pump", "Atlas is a pump at Cedar Plant. It may run only when inlet pressure exceeds 2 bar, except during maintenance. It must not run during maintenance. On 2025-01-01 its limit was 8 bar; from 2025-06-01 its limit is 6 bar. The calibration result is uncertain. The recorded literal is 123e4567-e89b-12d3-a456-426614174000.", "Atlas is a pump at Cedar Plant. It may run only when inlet pressure exceeds 2 bar, except during maintenance. It must not run during maintenance. On 2025-01-01 its limit was 8 bar; from 2025-06-01 its limit is 6 bar. The calibration result is uncertain. The recorded literal is 123e4567-e89b-12d3-a456-426614174000.", "Atlas pump", true, true}}
	case "F02":
		material = []authored{{"Atlas library", "Atlas is a software library maintained by Harbor Team.", "Atlas is a software library maintained by Harbor Team.", "Atlas library", true, true}, {"Atlas library", "It has no connection to the Atlas pump at Cedar Plant.", "Atlas is a software library maintained by Harbor Team. It has no connection to the Atlas pump at Cedar Plant.", "Atlas library", true, true}, {"Aero Link", "In this document Aero Link is a radio channel; it is a different object from the steel bracket Aero-Link.", "In this document Aero Link is a radio channel; Aero-Link is a steel bracket. These two names identify different objects.", "Aero Link", true, true}, {"Aero-Link", "Aero-Link is a steel bracket, a different object from radio channel Aero Link.", "In this document Aero Link is a radio channel; Aero-Link is a steel bracket. These two names identify different objects.", "Aero-Link", true, true}}
	case "F03":
		material = []authored{{"Report A", "Report A estimates that the Atlas pump calibration error on 2025-07-01 was 3 percent, with low confidence.", "Report A estimates that the Atlas pump calibration error on 2025-07-01 was 3 percent, with low confidence.", "Report A", true, true}, {"Atlas pump", "Report A estimates that the Atlas pump calibration error on 2025-07-01 was 3 percent, with low confidence.", "Report A estimates that the Atlas pump calibration error on 2025-07-01 was 3 percent, with low confidence.", "Atlas pump", true, true}, {"Valve V", "Valve V feeds the Atlas pump only while bypass mode is off. Valve V does not feed the pump while bypass mode is on.", "Valve V feeds the Atlas pump only while bypass mode is off. Valve V does not feed the pump while bypass mode is on.", "Valve V", true, true}}
	case "F04":
		material = []authored{{"Report B", "Report B estimates that the Atlas pump calibration error on 2025-07-01 was 5 percent, with low confidence.", "Report B estimates that the Atlas pump calibration error on 2025-07-01 was 5 percent, with low confidence.", "Report B", true, true}, {"Atlas pump", "Report B estimates that the Atlas pump calibration error on 2025-07-01 was 5 percent, with low confidence. The pump depends on Controller C from 2025-06-01; before that date it depended on Controller D.", "Report B estimates that the Atlas pump calibration error on 2025-07-01 was 5 percent, with low confidence.", "Atlas pump", true, true}, {"Valve V", "Valve V feeds the Atlas pump only while bypass mode is off.", "Valve V feeds the Atlas pump only while bypass mode is off.", "Valve V", true, true}}
	case "F05":
		material = []authored{{"Controller C", "Controller C provides the Cedar pump with a shutdown signal.", "Controller C provides the Cedar pump with a shutdown signal.", "Controller C", true, true}, {"Cedar pump", "The pump and Logger L were installed in the same room.", "The pump and Logger L were installed in the same room.", "Cedar pump", true, true}, {"Logger L", "The pump and Logger L were installed in the same room.", "The pump and Logger L were installed in the same room.", "Logger L", true, true}, {"This report", "This report does not state that the pump depends on Logger L.", "This report does not state that the pump depends on Logger L.", "This report", true, true}, {"Sensor S", "Whether Sensor S supports Controller C is unknown.", "Whether Sensor S supports Controller C is unknown.", "Sensor S", true, true}}
	case "F06":
		material = []authored{{"Cooling", "Cooling starts only above 30 degrees Celsius. Cooling is prohibited during maintenance.", "Cooling starts only above 30 degrees Celsius. Cooling is prohibited during maintenance.", "Cooling", true, true}}
	case "F07":
		material = []authored{{"Battery B", "Battery B has a capacity of 12 ampere-hours. Battery B powers Controller C.", "Battery B has a capacity of 12 ampere-hours. Battery B powers Controller C.", "Battery B", true, true}}
	case "F08":
		material = []authored{{"Valve V", "Valve V feeds the Atlas pump only while bypass mode is off.", "Valve V feeds the Atlas pump only while bypass mode is off.", "Valve V", true, true}}
	}
	rows := make([]dktypes.AnnotatedItem, len(material))
	proof := []map[string]any{}
	// The source-specific list above authors identities and semantic booleans;
	// this loop only serializes those explicitly declared associations.
	add := func(quote, subject string, index int, same, preserves bool) {
		proof = append(proof, map[string]any{"source_quote": quote, "subject_referent": subject, "checks": []any{map[string]any{"item_index": index, "item_referent": subject, "same_referent": same, "preserves_assertion": preserves, "reason": "explicit source-specific fixture association"}}})
	}
	for i, m := range material {
		rows[i] = dktypes.AnnotatedItem{Tag: dktypes.TagConcept, Name: m.name, Content: m.detail, Detail: m.detail, Confidence: .9}
		add(m.quote, m.subject, i, m.same, m.preserves)
	}
	if id == "F04" {
		add("The pump depends on Controller C from 2025-06-01; before that date it depended on Controller D.", "Atlas pump", 1, true, true)
	}
	return rows, proof
}

// 使用原来的八份补充原文检验 helper 的无损传递；期望和清单绝不作为原文提示。
// Exercise unchanged supplement originals through the helper, keeping expectations outside prompts.
// This deterministic client tests helper preservation, not a model's ability to judge semantics.
func TestV012AnnotationSupplementSourceBoundaryAndPreservation(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/v011/supplement.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture supplement
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Sources) != 8 || len(fixture.Cases) != 19 {
		t.Fatal("original finite denominator changed")
	}
	for _, source := range fixture.Sources {
		t.Run(source.SourceID, func(t *testing.T) {
			texts := []string{strings.TrimSpace(source.Content)}
			if source.SourceID == "F02" {
				// 原先按名称取胜者可能删除同名的不同事实；分开保留两组原文。
				// Name-based winner selection must not collapse these different source facts.
				parts := strings.SplitN(source.Content, "In this document", 2)
				if len(parts) != 2 {
					t.Fatal("immutable F02 source structure changed")
				}
				texts = []string{strings.TrimSpace(parts[0]), "In this document " + strings.TrimSpace(parts[1])}
			}
			if strings.Join(texts, " ") != strings.TrimSpace(source.Content) {
				t.Fatal("candidate fragments do not reconstruct the immutable original")
			}
			rows, proof := supplementSubjectRows(source.SourceID)
			if len(rows) == 0 {
				t.Fatal("missing authored subject fixture")
			}
			client := &v012SourceOnlyClient{t: t, source: source, rows: rows, proof: proof}
			result := annotation.NewDocumentProcessor(client, 0, -1, 0).Process(context.Background(), "fixtures", source.Path, source.Content)
			if result.Outcome != annotation.DocumentComplete || result.Document == nil || result.FilePath != source.Path || result.Document.FilePath != source.Path || result.CandidateID == "" || len(result.Document.Items) != len(rows) || len(client.requests) != 2 {
				t.Fatalf("lost source/candidate identity or facts: %+v", result)
			}
			for i, item := range result.Document.Items {
				if item.Content != rows[i].Content || item.Detail != rows[i].Detail {
					t.Fatalf("source distinction %d changed", i)
				}
			}
			if source.SourceID == "F02" && (len(result.Document.Items) != 4 || result.Document.Items[0].Name != result.Document.Items[1].Name || result.Document.Items[0].Content == result.Document.Items[1].Content) {
				t.Fatal("same-name distinct library facts collapsed")
			}
			if source.SourceID == "F01" && !strings.Contains(result.Document.Items[0].Detail, "123e4567-e89b-12d3-a456-426614174000") {
				t.Fatal("source UUID literal lost")
			}
		})
	}
}

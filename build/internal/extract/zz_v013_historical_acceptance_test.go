package extract

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// The raw saved response included the accepted member in the wrong category;
// its parser discard was formerly indistinguishable from a generation omission.
// This opt-in replay never calls a provider or writes the historical packet.
func TestV013HistoricalAcceptedMemberRecovery(t *testing.T) {
	packet := os.Getenv("CAIRN_V013_HISTORICAL_PACKET")
	if packet == "" {
		t.Skip("authorized retained packet not selected")
	}
	read := func(rel, want string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(packet, rel))
		if err != nil {
			t.Fatal("retained packet unavailable")
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != want {
			t.Fatal("retained packet identity mismatch")
		}
		return b
	}
	var docs []*dktypes.AnnotatedDocument
	if err := json.Unmarshal(read("build-dump/04_extract_input.json", "a8e9769b29bbdbd52cac17df2231ae7940aabe5290fb6aa21055a59dd75e23d4"), &docs); err != nil {
		t.Fatal("invalid retained04")
	}
	var saved Result
	if err := json.Unmarshal(read("build-dump/05_extract_result.json", "4f57187a79cb0e934f0551676a95694ef1bdc98cbff0624653cd252a924e9969"), &saved); err != nil {
		t.Fatal("invalid retained05")
	}
	before := ComputeFinalCoverage(&saved, docs)
	if before.InputUnits != 165 || before.CoveredUnits != 164 || before.InputIncidences != 172 || before.CoveredIncidences != 171 || len(before.UncoveredIDs) != 1 {
		t.Fatal("historical incidence boundary changed")
	}
	missing := before.UncoveredIDs[0]
	if fmt.Sprintf("%x", sha256.Sum256([]byte(missing))) != "97444630490e3fb40af72d589c3b0e4f368dbba05ade60247a75263773f3bf99" {
		t.Fatal("historical missing member changed")
	}
	trace := read("build-trace/requests.jsonl", "f909748b2ec7f9d5b98d2b8d02f19e7be0f3a156db642a88e2cc80c6b4606fd7")
	rawText := ""
	for _, line := range strings.Split(strings.TrimSpace(string(trace)), "\n") {
		var event struct {
			Event   string `json:"event"`
			Attempt int    `json:"attempt_id"`
			Body    string `json:"response_body"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			t.Fatal("invalid retained trace")
		}
		if event.Event != "response" || event.Attempt != 13 {
			continue
		}
		body, err := base64.StdEncoding.DecodeString(event.Body)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != "fecf5f9e6da66d4e1125e2e17c5d0d864e8f9fa51e511df19069579743cbbf90" {
			t.Fatal("response13 identity mismatch")
		}
		var wire struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				Finish string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(body, &wire) != nil || len(wire.Choices) != 1 || wire.Choices[0].Finish != "stop" {
			t.Fatal("invalid retained response13")
		}
		rawText = wire.Choices[0].Message.Content
	}
	if rawText == "" {
		t.Fatal("response13 not found")
	}
	var raw struct {
		Domains []Domain `json:"domains"`
	}
	if json.Unmarshal([]byte(rawText), &raw) != nil {
		t.Fatal("invalid retained model JSON")
	}
	var correction Domain
	found := false
	for _, d := range raw.Domains {
		for _, sd := range d.Subdomains {
			for _, n := range sd.Entities {
				if len(n.Members) == 1 && n.Members[0] == missing {
					if n.Label != "Concept" || n.Confidence != .95 {
						t.Fatal("category mismatch changed")
					}
					correction = Domain{Name: d.Name, Subdomains: []Subdomain{{Name: sd.Name, Concepts: []Node{n}}}}
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("raw response did not retain missing member in wrong category")
	}
	client := &v013AcceptanceClient{reply: func(_ context.Context, req llm.CompleteRequest, n int) (*llm.CompleteResponse, error) {
		if n == 1 {
			return &llm.CompleteResponse{Text: rawText, FinishReason: "stop"}, nil
		}
		if n != 2 || !strings.Contains(req.User, missing) {
			t.Fatal("recovery was not a single missing-member request")
		}
		for _, doc := range docs {
			for _, item := range doc.Items {
				if item.ID != missing && strings.Contains(req.User, "id="+item.ID+" ") {
					t.Fatal("successful sibling was resubmitted")
				}
			}
		}
		return &llm.CompleteResponse{Text: v013Wire(t, map[string]any{"domains": []Domain{correction}}), FinishReason: "stop"}, nil
	}}
	res, rpt, err := NewExtractor(client, 65536).ExtractWithCoverageOptions(context.Background(), docs, CoverageOptions{MaxRetries: 1})
	if err != nil {
		t.Fatal("historical helper replay failed")
	}
	if rpt.Outcome != "complete" || rpt.ExtractAttempts != 2 || len(rpt.Attempts) != 2 || !reflect.DeepEqual(rpt.Attempts[1].MemberIDs, []string{missing}) {
		t.Fatal("missing-member recovery did not complete in declared attempts")
	}
	final := ComputeFinalCoverage(res, docs)
	if final.CoveredUnits != 165 || final.CoveredIncidences != 172 || final.Outcome != "complete" {
		t.Fatal("recovery lost an accepted contribution")
	}
	t.Log("authentic raw response -> category filter171/172 -> scripted missing-only repair172/172; mapping evidence only")
}

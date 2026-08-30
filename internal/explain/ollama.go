package explain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Ollama talks to a local model server. No API key, no network egress,
// nothing leaves the machine.
type Ollama struct {
	Endpoint string
	Model    string
	client   *http.Client
}

// NewOllama returns a client. endpoint defaults to the usual local address.
func NewOllama(endpoint, model string) *Ollama {
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	return &Ollama{
		Endpoint: endpoint,
		Model:    model,
		client:   &http.Client{Timeout: 90 * time.Second},
	}
}

// Available reports whether the server is up and the model is present.
//
// Checked once at startup so the interface can say "no model, using
// templates" instead of discovering it one incident at a time.
func (o *Ollama) Available(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", o.Endpoint+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return false
	}
	for _, m := range out.Models {
		if m.Name == o.Model {
			return true
		}
	}
	return false
}

// Models lists what the server has.
func (o *Ollama) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", o.Endpoint+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

const systemPrompt = `You are a fraud analyst writing an incident note for a payments compliance log.

Rules, in order of importance:
1. Use ONLY numbers that appear in the facts given to you. Never estimate, round
   into a new figure, or introduce any quantity that is not listed. A wrong
   number in a compliance record is far worse than plain wording.
2. Do not speculate about intent, identity, or anything beyond the facts.
3. Write for a colleague who has not seen this incident: explain what the
   evidence means, not just what it is called.
4. Be brief. Narrative under 90 words.
5. The headline must carry the specifics — how many accounts, how much money,
   or how many failures. "Fraud Ring Detected" is useless in a log of a
   thousand incidents; "Laundering ring, 24 accounts, 86,37,301 rupees" is
   what somebody scanning that log actually needs.
6. If the facts say this is an infrastructure failure, do NOT describe it as
   fraud. The point of that note is that the two look alike and are not the
   same, and a record that confuses them would send analysts after customers
   for a fault that was the bank's.

Reply with JSON only, exactly these three keys:
{"headline": "...", "narrative": "...", "action": "..."}`

type ollamaReq struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	System string `json:"system"`
	Stream bool   `json:"stream"`
	Format string `json:"format"`
	// Think disables the reasoning phase on models that have one.
	//
	// Not an optimisation — a correctness fix. Reasoning models spend their
	// token budget thinking before they answer, and qwen3:1.7b at a 220-token
	// limit used the entire budget on reasoning and returned a literal empty
	// object every single time. The guard dutifully rejected it and everything
	// silently fell back to templates, which looked exactly like "the model is
	// unavailable". Turning thinking off produced correct output immediately.
	Think   bool           `json:"think"`
	Options map[string]any `json:"options"`
}

type ollamaResp struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// Explain asks the model for a narrative.
func (o *Ollama) Explain(ctx context.Context, f Facts) (Explanation, error) {
	body, err := json.Marshal(ollamaReq{
		Model:  o.Model,
		System: systemPrompt,
		Prompt: "Facts:\n" + Describe(f),
		Stream: false,
		Format: "json",
		Think:  false,
		Options: map[string]any{
			// Low temperature: this is a compliance record, not prose. It
			// also makes the cache hit far more often across identical runs.
			"temperature": 0.2,
			"num_ctx":     2048,
			"num_predict": 400,
		},
	})
	if err != nil {
		return Explanation{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", o.Endpoint+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return Explanation{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return Explanation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Explanation{}, fmt.Errorf("ollama: status %d", resp.StatusCode)
	}

	var out ollamaResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Explanation{}, err
	}

	var parsed struct {
		Headline  string `json:"headline"`
		Narrative string `json:"narrative"`
		Action    string `json:"action"`
	}
	if err := json.Unmarshal([]byte(out.Response), &parsed); err != nil {
		return Explanation{}, fmt.Errorf("ollama: model did not return usable JSON: %w", err)
	}

	return Explanation{
		IncidentID: f.IncidentID,
		Headline:   parsed.Headline,
		Narrative:  parsed.Narrative,
		Action:     parsed.Action,
		Source:     SourceLLM,
	}, nil
}

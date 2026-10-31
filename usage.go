package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// These are API short-context, standard-processing list prices per million
// tokens, not a statement about ChatGPT billing.
const lunaInputPerMillion = 0.10
const lunaOutputPerMillion = 0.50

type usageEstimate struct {
	Calls        int     `json:"calls"`
	InputBytes   int     `json:"input_bytes"`
	OutputBytes  int     `json:"output_bytes"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	EstimatedUSD float64 `json:"estimated_usd"`
	Incomplete   bool    `json:"incomplete"`
}

func estimateMCPUsage(r io.Reader) (usageEstimate, error) {
	var u usageEstimate
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 256<<10)
	for sc.Scan() {
		var e struct {
			Notice  string `json:"notice"`
			Request struct {
				Method string `json:"method"`
				Params struct {
					Arguments json.RawMessage `json:"arguments"`
				} `json:"params"`
			} `json:"request"`
			Response struct {
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			} `json:"response"`
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			u.Incomplete = true
			continue
		}
		if e.Notice != "" {
			u.Incomplete = true
			continue
		}
		if e.Request.Method != "tools/call" {
			continue
		}
		result := bytes.TrimSpace(e.Response.Result)
		if len(result) == 0 || bytes.Equal(result, []byte("null")) {
			result = bytes.TrimSpace(e.Response.Error)
		}
		if len(result) == 0 || bytes.Equal(result, []byte("null")) {
			u.Incomplete = true
			continue
		}
		u.Calls++
		if args := bytes.TrimSpace(e.Request.Params.Arguments); len(args) > 0 && !bytes.Equal(args, []byte("null")) {
			u.OutputBytes += len(args)
		}
		u.InputBytes += len(result)
	}
	if err := sc.Err(); err != nil {
		return u, err
	}
	// This deliberately coarse byte heuristic is not a tokenizer. UTF-8 language,
	// source code, JSON escaping and model tokenization can differ substantially.
	u.InputTokens = (u.InputBytes + 3) / 4
	u.OutputTokens = (u.OutputBytes + 3) / 4
	u.EstimatedUSD = (float64(u.InputTokens)*lunaInputPerMillion + float64(u.OutputTokens)*lunaOutputPerMillion) / 1_000_000
	return u, nil
}

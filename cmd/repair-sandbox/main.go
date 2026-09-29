package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/security"
)

const maxRequestBytes = 16 * 1024
const workspaceRoot = "/workspace"

type request struct {
	Operation string                       `json:"operation"`
	RecipeID  string                       `json:"recipe_id"`
	Binding   coderepair.RepositoryBinding `json:"binding"`
}

func parseRequest(data []byte) (request, error) {
	if len(data) == 0 || len(data) > maxRequestBytes {
		return request{}, errors.New("repair sandbox request exceeds its byte budget")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var req request
	if err := decoder.Decode(&req); err != nil {
		return request{}, errors.New("repair sandbox requires a valid operation object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return request{}, errors.New("repair sandbox request contains trailing data")
	}
	if req.Operation != "run_allowed_test" {
		return request{}, errors.New("repair sandbox accepts only run_allowed_test")
	}
	if _, err := sandbox.ResolveTestRecipe(req.Binding, req.RecipeID); err != nil {
		return request{}, err
	}
	return req, nil
}

func main() {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxRequestBytes+1))
	if err != nil {
		fail(err)
	}
	req, err := parseRequest(data)
	if err != nil {
		fail(err)
	}
	result, err := sandbox.RunAllowedTest(context.Background(), workspaceRoot, req.Binding, req.RecipeID)
	if err != nil {
		fail(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fail(err)
	}
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, security.Redact(err.Error()))
	os.Exit(2)
}

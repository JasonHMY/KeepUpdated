package privileged

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
)

type PreparationInput struct {
	TargetID         string `json:"targetID"`
	Kind             string `json:"kind"`
	PackagePath      string `json:"packagePath,omitempty"`
	InstallationPath string `json:"installationPath"`
	BundleID         string `json:"bundleID"`
	FromVersion      string `json:"fromVersion"`
}
type Preparation struct {
	TargetID         string    `json:"targetID"`
	Status           string    `json:"status"`
	Reason           string    `json:"reason"`
	Candidates       []Request `json:"candidates"`
	ExecutionBlocker string    `json:"executionBlocker"`
}

func (NativeClient) Prepare(ctx context.Context, input PreparationInput) (Preparation, error) {
	blocked := Preparation{TargetID: input.TargetID, Status: "blocked", Candidates: []Request{}}
	path, reason := bridge(ctx, false)
	if reason != "" {
		blocked.Reason = reason
		return blocked, nil
	}
	data, err := json.Marshal(input)
	if err != nil || len(data) > 65536 {
		return blocked, errors.New("invalid preparation input")
	}
	command := exec.CommandContext(ctx, path, "prepare")
	command.Stdin = bytes.NewReader(data)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	var out limitedOutput
	command.Stdout = &out
	if command.Run() != nil || out.overflow {
		return blocked, errors.New("native preparation unavailable")
	}
	var result Preparation
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || result.TargetID != input.TargetID {
		return blocked, errors.New("invalid native preparation response")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return blocked, errors.New("ambiguous native preparation response")
	}
	for _, request := range result.Candidates {
		if request.Validate() != nil {
			return blocked, errors.New("invalid prepared request")
		}
	}
	return result, nil
}

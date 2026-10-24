package mcpref

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const DefaultNPMRegistry = "https://registry.npmjs.org/"

// Candidate binds registry metadata to one configuration and its verified source.
// It says nothing about an installed or running server version.
type Candidate struct {
	Ecosystem        string `json:"ecosystem"`
	Package          string `json:"package"`
	Version          string `json:"version"`
	Reference        string `json:"reference"`
	SourceIdentity   string `json:"source_identity"`
	ConfigSHA256     string `json:"config_sha256"`
	MetadataSHA256   string `json:"metadata_sha256"`
	ConstraintResult string `json:"constraint_result"`
}

func (c Candidate) ID() string {
	data, _ := json.Marshal(c)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// Discovery observations are presentation evidence, separate from Candidate.ID.
type Discovery struct {
	Status              string     `json:"status"`
	Reason              string     `json:"reason"`
	ConfiguredReference string     `json:"configured_reference,omitempty"`
	ConfiguredVersion   string     `json:"configured_version,omitempty"`
	ObservedAt          string     `json:"observed_at,omitempty"`
	Candidate           *Candidate `json:"candidate,omitempty"`
	CandidateID         string     `json:"candidate_id,omitempty"`
}

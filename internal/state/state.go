// Package state provides persistent migration state for resumable runs.
package state

import (
	"encoding/json"
	"os"
	"time"
)

const defaultStateFile = "migration_state.json"

// Error records a single migration error.
type Error struct {
	Type      string `json:"type"`
	Key       string `json:"key"`
	Error     string `json:"error"`
	Timestamp string `json:"timestamp"`
}

// Data holds the persisted migration state.
type Data struct {
	WarehouseSetup     bool     `json:"warehouse_setup"`
	DataSourcesCreated []string `json:"data_sources_created"`
	Errors             []Error  `json:"errors"`
}

// MigrationState tracks which entities have been migrated, enabling resume.
type MigrationState struct {
	path string
	data Data
}

// NewMigrationState creates a new state tracker. If resume is true, existing
// state is loaded from disk.
func NewMigrationState(resume bool) *MigrationState {
	s := &MigrationState{path: defaultStateFile}
	if resume {
		s.load()
	}
	return s
}

func (s *MigrationState) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(raw, &s.data)
}

// Save persists the current state to disk.
func (s *MigrationState) Save() {
	raw, _ := json.MarshalIndent(s.data, "", "  ")
	_ = os.WriteFile(s.path, raw, 0o644)
}

// IsWarehouseDone returns true if warehouse setup completed in a previous run.
func (s *MigrationState) IsWarehouseDone() bool {
	return s.data.WarehouseSetup
}

// SetWarehouseDone marks warehouse setup as complete.
func (s *MigrationState) SetWarehouseDone() {
	s.data.WarehouseSetup = true
	s.Save()
}

// IsDataSourceDone returns true if the data source was created in a previous run.
func (s *MigrationState) IsDataSourceDone(key string) bool {
	for _, k := range s.data.DataSourcesCreated {
		if k == key {
			return true
		}
	}
	return false
}

// MarkDataSourceDone records a data source as created.
func (s *MigrationState) MarkDataSourceDone(key string) {
	s.data.DataSourcesCreated = append(s.data.DataSourcesCreated, key)
	s.Save()
}

// AddError records a migration error.
func (s *MigrationState) AddError(entityType, key, errMsg string) {
	s.data.Errors = append(s.data.Errors, Error{
		Type:      entityType,
		Key:       key,
		Error:     errMsg,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	s.Save()
}

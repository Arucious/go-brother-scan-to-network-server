package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/destination"
)

const maxJobs = 50

// Job is one scan run.
type Job struct {
	ID       int         `json:"id"`
	Started  time.Time   `json:"started"`
	Finished time.Time   `json:"finished,omitzero"`
	Target   string      `json:"target"`
	Function string      `json:"function"`
	Trigger  string      `json:"trigger"` // button, web or custom
	Status   string      `json:"status"`  // running, done, failed, interrupted
	Settings JobSettings `json:"settings"`
	Pages    int         `json:"pages"`
	Files    []JobFile   `json:"files,omitempty"`
	Error    string      `json:"error,omitempty"`
}

// JobSettings describes how a job was scanned and stored.
type JobSettings struct {
	// From is where the settings came from: panel, config or custom.
	From     string `json:"from"`
	FileType string `json:"file_type"`
	Color    string `json:"color"`
	DPI      int    `json:"dpi"`
	PageSize string `json:"page_size"`
	Source   string `json:"source"`
}

// JobFile is one stored file of a job.
type JobFile struct {
	// Name is relative to the target's destination root.
	Name     string `json:"name"`
	Location string `json:"location"`
	Size     int    `json:"size"`
}

// Base returns the file name without directories.
func (f JobFile) Base() string { return path.Base(f.Name) }

// SetHistoryFile loads job history from path and saves it there from now on.
func (s *Service) SetHistoryFile(file string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyFile = file
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var jobs []*Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return fmt.Errorf("parse %s: %w", file, err)
	}
	for _, j := range jobs {
		if j.Status == "running" {
			j.Status = "interrupted"
		}
		s.nextJobID = max(s.nextJobID, j.ID)
	}
	s.jobs = jobs
	return nil
}

// saveHistoryLocked writes the job history; s.mu must be held.
func (s *Service) saveHistoryLocked() {
	if s.historyFile == "" {
		return
	}
	data, err := json.MarshalIndent(s.jobs, "", "  ")
	if err == nil {
		tmp := s.historyFile + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, s.historyFile)
		}
	}
	if err != nil {
		s.log.Warn("could not save job history", "file", s.historyFile, "err", err)
	}
}

func (s *Service) startJob(target, function, trigger string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextJobID++
	job := &Job{ID: s.nextJobID, Started: time.Now(), Target: target, Function: function, Trigger: trigger, Status: "running"}
	s.jobs = append(s.jobs, job)
	if len(s.jobs) > maxJobs {
		s.jobs = s.jobs[len(s.jobs)-maxJobs:]
	}
	s.saveHistoryLocked()
	return job
}

func (s *Service) finishJob(job *Job, out outcome, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job.Finished = time.Now()
	job.Settings = out.settings
	job.Pages = out.pages
	for _, f := range out.files {
		job.Files = append(job.Files, JobFile{Name: f.Name, Location: f.Location, Size: f.Size})
	}
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
	} else {
		job.Status = "done"
	}
	s.saveHistoryLocked()
}

// OpenJobFile opens file index of job id through the target's destination.
func (s *Service) OpenJobFile(ctx context.Context, id, index int) (io.ReadCloser, int64, JobFile, error) {
	s.mu.Lock()
	var file JobFile
	var targetName string
	found := false
	for _, j := range s.jobs {
		if j.ID == id && index >= 0 && index < len(j.Files) {
			file, targetName, found = j.Files[index], j.Target, true
		}
	}
	cfg := s.cfg
	s.mu.Unlock()
	if !found {
		return nil, 0, file, os.ErrNotExist
	}
	if cfg == nil {
		return nil, 0, file, errors.New("server is not configured")
	}
	target, ok := cfg.Target(targetName)
	if !ok {
		return nil, 0, file, fmt.Errorf("target %q no longer exists", targetName)
	}
	dest, err := destination.New(target.Destination)
	if err != nil {
		return nil, 0, file, err
	}
	rc, size, err := dest.Open(ctx, file.Name)
	return rc, size, file, err
}

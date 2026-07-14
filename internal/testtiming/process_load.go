package testtiming

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type GoProcess struct {
	PID     int    `json:"pid"`
	Parent  int    `json:"parent_pid"`
	Command string `json:"command"`
}

type ProcessLoadEvidence struct {
	Method                 string      `json:"method"`
	SampleInterval         string      `json:"sample_interval"`
	Samples                int         `json:"samples"`
	MaxGoProcesses         int         `json:"max_go_processes"`
	PeakProcesses          []GoProcess `json:"peak_processes"`
	MaxExternalGoProcesses int         `json:"max_external_go_processes"`
	PeakExternalProcesses  []GoProcess `json:"peak_external_processes"`
	OverlapDetected        bool        `json:"overlap_detected"`
}

type processLoadSampler struct {
	mu       sync.Mutex
	evidence ProcessLoadEvidence
	stop     chan struct{}
	done     chan struct{}
}

func startProcessLoadSampler(interval time.Duration) *processLoadSampler {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	s := &processLoadSampler{evidence: ProcessLoadEvidence{Method: "ps-process-tree-v2", SampleInterval: interval.String(), PeakProcesses: []GoProcess{}, PeakExternalProcesses: []GoProcess{}}, stop: make(chan struct{}), done: make(chan struct{})}
	s.sample()
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.sample()
			}
		}
	}()
	return s
}

func (s *processLoadSampler) finish() ProcessLoadEvidence {
	close(s.stop)
	<-s.done
	s.sample()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evidence
}

func (s *processLoadSampler) sample() {
	cmd := exec.CommandContext(context.Background(), "ps", "-axo", "pid=,ppid=,comm=")
	output, err := cmd.Output()
	if err != nil {
		return
	}
	processes, external := classifyGoProcesses(output, os.Getpid())
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evidence.Samples++
	if len(processes) > s.evidence.MaxGoProcesses {
		s.evidence.MaxGoProcesses = len(processes)
		s.evidence.PeakProcesses = processes
	}
	if len(external) > s.evidence.MaxExternalGoProcesses {
		s.evidence.MaxExternalGoProcesses = len(external)
		s.evidence.PeakExternalProcesses = external
	}
	s.evidence.OverlapDetected = s.evidence.MaxExternalGoProcesses > 0
}

func parseGoProcesses(output []byte) []GoProcess {
	processes, _ := classifyGoProcesses(output, -1)
	return processes
}

func classifyGoProcesses(output []byte, rootPID int) ([]GoProcess, []GoProcess) {
	lines := bytes.Split(output, []byte{'\n'})
	parents := make(map[int]int)
	goProcesses := make([]GoProcess, 0)
	for _, line := range lines {
		fields := strings.Fields(string(line))
		if len(fields) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil {
			continue
		}
		parents[pid] = parent
		command := filepath.Base(fields[2])
		if command == "go" || command == "compile" || command == "link" || command == "vet" || strings.HasSuffix(command, ".test") {
			goProcesses = append(goProcesses, GoProcess{PID: pid, Parent: parent, Command: command})
		}
	}
	if rootPID < 0 {
		return goProcesses, nil
	}
	related := map[int]bool{rootPID: true}
	for pid := rootPID; pid > 1; {
		pid = parents[pid]
		if pid <= 0 {
			break
		}
		related[pid] = true
	}
	descendants := map[int]bool{rootPID: true}
	changed := true
	for changed {
		changed = false
		for pid, parent := range parents {
			if descendants[parent] && !descendants[pid] {
				descendants[pid] = true
				related[pid] = true
				changed = true
			}
		}
	}
	external := make([]GoProcess, 0)
	for _, process := range goProcesses {
		if !related[process.PID] {
			external = append(external, process)
		}
	}
	return goProcesses, external
}

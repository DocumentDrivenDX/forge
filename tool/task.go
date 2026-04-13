package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// TaskStatus represents the status of a tracked task.
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
)

// Task represents a tracked subtask within an agent run.
type Task struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Status      TaskStatus `json:"status"`
	Order       int        `json:"order"` // for ordering tasks in a plan
}

// TaskStore persists task state within a single agent run.
type TaskStore struct {
	mu     sync.RWMutex
	tasks  map[string]*Task
	nextID int
}

// NewTaskStore creates a new empty task store.
func NewTaskStore() *TaskStore {
	return &TaskStore{
		tasks:  make(map[string]*Task),
		nextID: 1,
	}
}

// Create adds a new pending task and returns its ID.
func (s *TaskStore) Create(title string, description string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := fmt.Sprintf("task-%d", s.nextID)
	s.nextID++

	task := &Task{
		ID:          id,
		Title:       title,
		Description: description,
		Status:      TaskStatusPending,
		Order:       len(s.tasks),
	}
	s.tasks[id] = task
	return id
}

// Update changes the status of an existing task.
func (s *TaskStore) Update(id string, status TaskStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("task %s not found", id)
	}
	task.Status = status
	return nil
}

// Get retrieves a task by ID.
func (s *TaskStore) Get(id string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	task, ok := s.tasks[id]
	if !ok {
		return nil, fmt.Errorf("task %s not found", id)
	}
	return task, nil
}

// List returns all tasks sorted by their Order field.
func (s *TaskStore) List() []*Task {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		result = append(result, task)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Order < result[j].Order
	})
	return result
}

// TaskParams are the parameters for task tool operations.
type TaskParams struct {
	Operation   string `json:"operation"` // create, update, list, get
	ID          string `json:"id,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
}

// TaskTool provides task tracking capabilities for multi-step planning.
type TaskTool struct {
	Store *TaskStore
}

func (t *TaskTool) Name() string { return "task" }
func (t *TaskTool) Description() string {
	return "Track subtasks in a multi-step plan: create, update, get, list."
}
func (t *TaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"operation": {"type": "string", "enum": ["create", "update", "get", "list"], "description": "The operation to perform"},
			"id": {"type": "string", "description": "Task ID (required for update/get, ignored for create/list)"},
			"title": {"type": "string", "description": "Task title (required for create)"},
			"description": {"type": "string", "description": "Optional task description"},
			"status": {"type": "string", "enum": ["pending", "in_progress", "completed"], "description": "New status (required for update)"}
		},
		"required": ["operation"]
	}`)
}

func (t *TaskTool) Parallel() bool { return false }

func (t *TaskTool) Execute(_ context.Context, params json.RawMessage) (string, error) {
	var p TaskParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("task: invalid params: %w", err)
	}

	switch p.Operation {
	case "create":
		if p.Title == "" {
			return "", fmt.Errorf("task create: title is required")
		}
		id := t.Store.Create(p.Title, p.Description)
		task, _ := t.Store.Get(id)
		result, _ := json.MarshalIndent(task, "", "  ")
		return string(result), nil

	case "update":
		if p.ID == "" {
			return "", fmt.Errorf("task update: id is required")
		}
		if p.Status == "" {
			return "", fmt.Errorf("task update: status is required")
		}
		var status TaskStatus = TaskStatus(p.Status)
		switch status {
		case TaskStatusPending, TaskStatusInProgress, TaskStatusCompleted:
			// valid
		default:
			return "", fmt.Errorf("task update: invalid status %q", p.Status)
		}
		if err := t.Store.Update(p.ID, status); err != nil {
			return "", err
		}
		task, _ := t.Store.Get(p.ID)
		result, _ := json.MarshalIndent(task, "", "  ")
		return string(result), nil

	case "get":
		if p.ID == "" {
			return "", fmt.Errorf("task get: id is required")
		}
		task, err := t.Store.Get(p.ID)
		if err != nil {
			return "", err
		}
		result, _ := json.MarshalIndent(task, "", "  ")
		return string(result), nil

	case "list":
		tasks := t.Store.List()
		result, _ := json.MarshalIndent(tasks, "", "  ")
		return string(result), nil

	default:
		return "", fmt.Errorf("task: unknown operation %q", p.Operation)
	}
}

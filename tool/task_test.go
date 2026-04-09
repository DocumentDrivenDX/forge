package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestTaskStore_Create(t *testing.T) {
	store := NewTaskStore()

	id := store.Create("Test Task", "A test description")
	if id != "task-1" {
		t.Errorf("Expected ID task-1, got %s", id)
	}

	task, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if task.Title != "Test Task" {
		t.Errorf("Expected title 'Test Task', got %q", task.Title)
	}
	if task.Description != "A test description" {
		t.Errorf("Expected description 'A test description', got %q", task.Description)
	}
	if task.Status != TaskStatusPending {
		t.Errorf("Expected status pending, got %s", task.Status)
	}
	if task.Order != 0 {
		t.Errorf("Expected order 0, got %d", task.Order)
	}
}

func TestTaskStore_Create_Multiple(t *testing.T) {
	store := NewTaskStore()

	id1 := store.Create("First Task", "")
	id2 := store.Create("Second Task", "Desc 2")
	id3 := store.Create("Third Task", "Desc 3")

	if id1 != "task-1" || id2 != "task-2" || id3 != "task-3" {
		t.Errorf("Expected sequential IDs, got %s, %s, %s", id1, id2, id3)
	}

	tasks := store.List()
	if len(tasks) != 3 {
		t.Fatalf("Expected 3 tasks, got %d", len(tasks))
	}

	if tasks[0].Order != 0 || tasks[1].Order != 1 || tasks[2].Order != 2 {
		t.Errorf("Expected orders 0,1,2, got %d,%d,%d", tasks[0].Order, tasks[1].Order, tasks[2].Order)
	}
}

func TestTaskStore_Update(t *testing.T) {
	store := NewTaskStore()
	id := store.Create("Test Task", "")

	if err := store.Update(id, TaskStatusInProgress); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	task, _ := store.Get(id)
	if task.Status != TaskStatusInProgress {
		t.Errorf("Expected status in_progress, got %s", task.Status)
	}

	if err := store.Update(id, TaskStatusCompleted); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	task, _ = store.Get(id)
	if task.Status != TaskStatusCompleted {
		t.Errorf("Expected status completed, got %s", task.Status)
	}
}

func TestTaskStore_Update_NotFound(t *testing.T) {
	store := NewTaskStore()

	err := store.Update("nonexistent", TaskStatusCompleted)
	if err == nil {
		t.Error("Expected error for updating nonexistent task")
	}
}

func TestTaskStore_Get(t *testing.T) {
	store := NewTaskStore()
	id := store.Create("Test Task", "Description")

	task, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if task.ID != id || task.Title != "Test Task" {
		t.Errorf("Task mismatch: %+v", task)
	}
}

func TestTaskStore_Get_NotFound(t *testing.T) {
	store := NewTaskStore()

	_, err := store.Get("nonexistent")
	if err == nil {
		t.Error("Expected error for getting nonexistent task")
	}
}

func TestTaskStore_List(t *testing.T) {
	store := NewTaskStore()

	tasks := store.List()
	if len(tasks) != 0 {
		t.Errorf("Expected empty list, got %d tasks", len(tasks))
	}

	id1 := store.Create("First", "")
	id2 := store.Create("Second", "")

	tasks = store.List()
	if len(tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(tasks))
	}

	// Verify both IDs are present
	found1, found2 := false, false
	for _, task := range tasks {
		if task.ID == id1 {
			found1 = true
		}
		if task.ID == id2 {
			found2 = true
		}
	}
	if !found1 || !found2 {
		t.Error("Not all created tasks found in list")
	}
}

func TestTaskTool_Create(t *testing.T) {
	store := NewTaskStore()
	tool := &TaskTool{Store: store}

	params, _ := json.Marshal(TaskParams{
		Operation:   "create",
		Title:       "My Task",
		Description: "Test description",
	})

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var task Task
	if err := json.Unmarshal([]byte(result), &task); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	if task.Title != "My Task" || task.Status != TaskStatusPending {
		t.Errorf("Task mismatch: %+v", task)
	}
}

func TestTaskTool_Create_NoTitle(t *testing.T) {
	store := NewTaskStore()
	tool := &TaskTool{Store: store}

	params, _ := json.Marshal(TaskParams{
		Operation: "create",
	})

	_, err := tool.Execute(context.Background(), params)
	if err == nil {
		t.Error("Expected error for create without title")
	}
}

func TestTaskTool_Update(t *testing.T) {
	store := NewTaskStore()
	id := store.Create("Original Title", "")
	tool := &TaskTool{Store: store}

	params, _ := json.Marshal(TaskParams{
		Operation: "update",
		ID:        id,
		Status:    "completed",
	})

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var task Task
	json.Unmarshal([]byte(result), &task)
	if task.Status != TaskStatusCompleted {
		t.Errorf("Expected completed status, got %s", task.Status)
	}
}

func TestTaskTool_Update_NoID(t *testing.T) {
	store := NewTaskStore()
	tool := &TaskTool{Store: store}

	params, _ := json.Marshal(TaskParams{
		Operation: "update",
		Status:    "completed",
	})

	_, err := tool.Execute(context.Background(), params)
	if err == nil {
		t.Error("Expected error for update without ID")
	}
}

func TestTaskTool_Update_NoStatus(t *testing.T) {
	store := NewTaskStore()
	id := store.Create("Test", "")
	tool := &TaskTool{Store: store}

	params, _ := json.Marshal(TaskParams{
		Operation: "update",
		ID:        id,
	})

	_, err := tool.Execute(context.Background(), params)
	if err == nil {
		t.Error("Expected error for update without status")
	}
}

func TestTaskTool_Update_InvalidStatus(t *testing.T) {
	store := NewTaskStore()
	id := store.Create("Test", "")
	tool := &TaskTool{Store: store}

	params, _ := json.Marshal(TaskParams{
		Operation: "update",
		ID:        id,
		Status:    "invalid_status",
	})

	_, err := tool.Execute(context.Background(), params)
	if err == nil {
		t.Error("Expected error for invalid status")
	}
}

func TestTaskTool_Get(t *testing.T) {
	store := NewTaskStore()
	id := store.Create("Test Task", "Description")
	tool := &TaskTool{Store: store}

	params, _ := json.Marshal(TaskParams{
		Operation: "get",
		ID:        id,
	})

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var task Task
	json.Unmarshal([]byte(result), &task)
	if task.Title != "Test Task" || task.Description != "Description" {
		t.Errorf("Task mismatch: %+v", task)
	}
}

func TestTaskTool_Get_NoID(t *testing.T) {
	store := NewTaskStore()
	tool := &TaskTool{Store: store}

	params, _ := json.Marshal(TaskParams{
		Operation: "get",
	})

	_, err := tool.Execute(context.Background(), params)
	if err == nil {
		t.Error("Expected error for get without ID")
	}
}

func TestTaskTool_List(t *testing.T) {
	store := NewTaskStore()
	store.Create("Task 1", "")
	store.Create("Task 2", "Desc 2")
	tool := &TaskTool{Store: store}

	params, _ := json.Marshal(TaskParams{
		Operation: "list",
	})

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var tasks []Task
	json.Unmarshal([]byte(result), &tasks)
	if len(tasks) != 2 {
		t.Errorf("Expected 2 tasks, got %d", len(tasks))
	}
}

func TestTaskTool_UnknownOperation(t *testing.T) {
	store := NewTaskStore()
	tool := &TaskTool{Store: store}

	params, _ := json.Marshal(TaskParams{
		Operation: "unknown",
	})

	_, err := tool.Execute(context.Background(), params)
	if err == nil {
		t.Error("Expected error for unknown operation")
	}
}

func TestTaskTool_InvalidJSON(t *testing.T) {
	store := NewTaskStore()
	tool := &TaskTool{Store: store}

	_, err := tool.Execute(context.Background(), json.RawMessage(`invalid json`))
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestTaskTool_FullWorkflow(t *testing.T) {
	store := NewTaskStore()
	tool := &TaskTool{Store: store}

	// Create multiple tasks
	create1, _ := json.Marshal(TaskParams{Operation: "create", Title: "Setup environment"})
	r1, err := tool.Execute(context.Background(), create1)
	if err != nil {
		t.Fatalf("Create 1 failed: %v", err)
	}

	var task1 Task
	json.Unmarshal([]byte(r1), &task1)
	if task1.Status != TaskStatusPending {
		t.Errorf("Expected pending, got %s", task1.Status)
	}

	create2, _ := json.Marshal(TaskParams{Operation: "create", Title: "Implement feature"})
	r2, err := tool.Execute(context.Background(), create2)
	if err != nil {
		t.Fatalf("Create 2 failed: %v", err)
	}

	var task2 Task
	json.Unmarshal([]byte(r2), &task2)

	// Update first task to in_progress
	update1, _ := json.Marshal(TaskParams{Operation: "update", ID: task1.ID, Status: "in_progress"})
	r3, err := tool.Execute(context.Background(), update1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	var updated Task
	json.Unmarshal([]byte(r3), &updated)
	if updated.Status != TaskStatusInProgress {
		t.Errorf("Expected in_progress, got %s", updated.Status)
	}

	// List all tasks
	listParams, _ := json.Marshal(TaskParams{Operation: "list"})
	r4, err := tool.Execute(context.Background(), listParams)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	var tasks []Task
	json.Unmarshal([]byte(r4), &tasks)
	if len(tasks) != 2 {
		t.Errorf("Expected 2 tasks, got %d", len(tasks))
	}

	// Complete first task
	updateComplete, _ := json.Marshal(TaskParams{Operation: "update", ID: task1.ID, Status: "completed"})
	r5, err := tool.Execute(context.Background(), updateComplete)
	if err != nil {
		t.Fatalf("Update complete failed: %v", err)
	}

	var completed Task
	json.Unmarshal([]byte(r5), &completed)
	if completed.Status != TaskStatusCompleted {
		t.Errorf("Expected completed, got %s", completed.Status)
	}

	// Get second task and update it
	getParams, _ := json.Marshal(TaskParams{Operation: "get", ID: task2.ID})
	r6, err := tool.Execute(context.Background(), getParams)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var retrieved Task
	json.Unmarshal([]byte(r6), &retrieved)
	if retrieved.Title != "Implement feature" {
		t.Errorf("Expected 'Implement feature', got %q", retrieved.Title)
	}
}

func TestTaskStore_Concurrency(t *testing.T) {
	store := NewTaskStore()
	done := make(chan bool)

	// Concurrent creates
	for i := 0; i < 10; i++ {
		go func(n int) {
			id := store.Create(fmt.Sprintf("Task %d", n), "")
			store.Update(id, TaskStatusCompleted)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	tasks := store.List()
	if len(tasks) != 10 {
		t.Errorf("Expected 10 tasks, got %d", len(tasks))
	}
}

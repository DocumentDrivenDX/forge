package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/DocumentDrivenDX/agent/benchscore"
)

func main() {
	tasksJSONL := flag.String("tasks-jsonl", "", "Path to benchmark task results JSONL")
	flag.Parse()

	if *tasksJSONL == "" {
		fmt.Fprintln(os.Stderr, "usage: ddx-agent-benchscore -tasks-jsonl <path>")
		os.Exit(2)
	}

	report, err := benchscore.ScoreTaskResultsJSONL(*tasksJSONL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ddx-agent-benchscore: %v\n", err)
		os.Exit(1)
	}

	data, err := json.Marshal(report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ddx-agent-benchscore: marshal: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/agent"
)

var (
	agentPrompt string
	agentFormat string
	agentInput  string
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Run agent framework for memory operations",
	Long: `Agent mode exposes memory operations as tools.
Use --prompt to inject context, --input for tool calls via JSON file, or pipe JSON to stdin.

Tool call format (JSON array):
  [{"name": "memory_write", "params": {"content": "...", "category": "knowledge"}}]`,
	Example: `  memory agent --list-tools
  memory agent --prompt "user preferences"
  echo '[{"name":"memory_write","params":{"content":"hello","type":"long"}}]' | memory agent`,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := getStore()
		if err != nil {
			return err
		}

		a := agent.New(s)
		agent.RegisterAll(a, s)

		if listTools, _ := cmd.Flags().GetBool("list-tools"); listTools {
			return printTools(a)
		}

		sess := agent.NewSession(a)

		if agentPrompt != "" {
			if err := sess.InjectContext(agentPrompt); err != nil {
				return fmt.Errorf("inject context: %w", err)
			}
		}

		data, err := readInput(cmd)
		if err != nil {
			return fmt.Errorf("read input: %w", err)
		}
		if data == nil {
			if sess.Context() != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Context: %s\n", sess.Context())
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "No input. Use --input, stdin, or --list-tools.")
			}
			return nil
		}

		result, err := sess.RunJSON(cmd.Context(), data)
		if err != nil {
			return err
		}

		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	},
}

func init() {
	agentCmd.Flags().StringVar(&agentPrompt, "prompt", "", "Inject memory context by searching with query")
	agentCmd.Flags().StringVar(&agentFormat, "format", "json", "Output format: json")
	agentCmd.Flags().StringVar(&agentInput, "input", "", "Input JSON file for tool calls")
	agentCmd.Flags().Bool("list-tools", false, "List all available tools and their schemas")
	rootCmd.AddCommand(agentCmd)
}

func printTools(a *agent.Agent) error {
	infos := a.ListTools()
	out, _ := json.MarshalIndent(infos, "", "  ")
	fmt.Println(string(out))
	return nil
}

func readInput(cmd *cobra.Command) ([]byte, error) {
	if agentInput != "" {
		return os.ReadFile(agentInput)
	}

	stat, err := os.Stdin.Stat()
	if err != nil {
		return nil, nil
	}
	if stat.Mode()&os.ModeCharDevice != 0 {
		return nil, nil
	}

	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	return data, nil
}

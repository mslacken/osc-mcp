package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"

	"github.com/openSUSE/osc-mcp/internal/pkg/osc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var (
	cfgFile string
	debug   bool
	output  string
	filters []string
	fields  []string
)

var rootCmd = &cobra.Command{
	Use:   "request_getter [request_id]",
	Short: "Get information about an OBS request",
	Long: `Get information about an OBS request and optionally filter or extract specific fields.

Filtering (-f, --filter):
  You can filter requests by specifying key=value pairs (e.g., -f state=accepted).
  If the request does not match ALL filters, the program exits with status 1 and prints an error object (e.g., {"error": "failed"}).
  For array fields (like actions or reviews), the filter matches if ANY element in the array matches.
  All values in the request object can be filtered using dot notation (e.g., actions.source.package=enigma).

Output Fields (-F, --fields):
  Specify a comma-separated list of fields to output if the request matches the filters.
  Values are output as a structured JSON/YAML map where the keys are the requested fields and the values are arrays of extracted strings.

Available Fields (traversable via dot notation, case-insensitive):
  - id, creator (alias: user), created, description
  - state
    - name (alias: state), who, when, superseded
  - actions (alias: action)
    - type (alias: type), source, target, persons, groups
    - source.project (alias: source.project)
    - source.package (alias: package)
    - source.rev (alias: revision, rev)
    - target.project (alias: target, repository)
    - target.package
  - reviews (alias: review)
    - state, who, when, byuser, bygroup, byproject, bypackage
  - histories (alias: history)
    - who, when, comment

Note: Array indexing is automatic. 'actions.type' returns the types of all actions.`,
	Args: cobra.ExactArgs(1),
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		logLevel := slog.LevelInfo
		if debug {
			logLevel = slog.LevelDebug
		}
		handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: logLevel,
		})
		slog.SetDefault(slog.New(handler))

		if cfgFile != "" {
			viper.SetConfigFile(cfgFile)
			if err := viper.ReadInConfig(); err != nil {
				slog.Warn("failed to read config file", "path", cfgFile, "error", err)
			}
		}
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		requestID := args[0]

		credsVal, err := osc.GetCredentials()
		if err != nil {
			return fmt.Errorf("failed to get osc credentials: %w", err)
		}
		creds := &credsVal

		ctx := context.Background()
		_, request, err := creds.GetRequest(ctx, nil, osc.GetRequestCmd{Id: requestID})
		if err != nil {
			return fmt.Errorf("failed to get request %s: %w", requestID, err)
		}

		if len(filters) > 0 {
			if !matchFilters(request, filters) {
				if output == "yaml" {
					fmt.Println("error: failed")
				} else {
					fmt.Println(`{"error": "failed"}`)
				}
				os.Exit(1)
			}
		}

		if len(fields) > 0 {
			return printFields(request, fields, output)
		}

		var out []byte
		if output == "yaml" {
			out, err = yaml.Marshal(request)
		} else {
			out, err = json.MarshalIndent(request, "", "  ")
		}

		if err != nil {
			return fmt.Errorf("failed to marshal output: %w", err)
		}

		fmt.Println(string(out))
		return nil
	},
}

func resolveAlias(path string) string {
	aliases := map[string]string{
		"user":           "creator",
		"state":          "state.name",
		"type":           "actions.type",
		"action.type":    "actions.type",
		"package":        "actions.source.package",
		"source.package": "actions.source.package",
		"target":         "actions.target.project",
		"repository":     "actions.target.project",
		"target.project": "actions.target.project",
		"rev":            "actions.source.rev",
		"revision":       "actions.source.rev",
		"source.project": "actions.source.project",
	}
	if resolved, ok := aliases[strings.ToLower(path)]; ok {
		return resolved
	}
	return path
}

func extractValues(v reflect.Value, parts []string) []string {
	if v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}

	if len(parts) == 0 {
		return []string{fmt.Sprintf("%v", v.Interface())}
	}

	part := strings.ReplaceAll(strings.ToLower(parts[0]), "_", "")

	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			fn := strings.ToLower(field.Name)
			if fn == part || fn == part+"s" || fn+"s" == part {
				return extractValues(v.Field(i), parts[1:])
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		var res []string
		for i := 0; i < v.Len(); i++ {
			res = append(res, extractValues(v.Index(i), parts)...)
		}
		return res
	default:
		return nil
	}
}

func getValues(req *osc.Request, path string) []string {
	path = resolveAlias(path)
	parts := strings.Split(path, ".")
	return extractValues(reflect.ValueOf(req), parts)
}

func matchFilters(req *osc.Request, filters []string) bool {
	for _, f := range filters {
		parts := strings.SplitN(f, "=", 2)
		if len(parts) != 2 {
			slog.Warn("invalid filter format, expected key=value", "filter", f)
			return false
		}
		key, expectedVal := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])

		values := getValues(req, key)
		matched := false
		for _, val := range values {
			if val == expectedVal {
				matched = true
				break
			}
		}

		if !matched {
			return false
		}
	}
	return true
}

func printFields(req *osc.Request, fields []string, outputFormat string) error {
	res := make(map[string][]string)
	for _, f := range fields {
		f = strings.TrimSpace(f)
		res[f] = getValues(req, f)
	}

	var out []byte
	var err error
	if outputFormat == "yaml" {
		out, err = yaml.Marshal(res)
	} else {
		out, err = json.MarshalIndent(res, "", "  ")
	}

	if err != nil {
		return fmt.Errorf("failed to marshal fields: %w", err)
	}

	fmt.Println(string(out))
	return nil
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default is $HOME/.oscrc)")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging")
	rootCmd.PersistentFlags().StringP("api", "a", "", "OBS API URL")
	rootCmd.Flags().StringVarP(&output, "output", "o", "json", "Output format (json or yaml)")
	rootCmd.Flags().StringSliceVarP(&filters, "filter", "f", nil, "Filter request (e.g. state=accepted, target=openSUSE:Factory)")
	rootCmd.Flags().StringSliceVarP(&fields, "fields", "F", nil, "Fields to output if matched (e.g. package, repository, revision)")

	viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	viper.BindPFlag("api", rootCmd.PersistentFlags().Lookup("api"))
	viper.BindPFlag("output", rootCmd.Flags().Lookup("output"))
}

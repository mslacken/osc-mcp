package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/openSUSE/osc-mcp/internal/pkg/osc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var (
	cfgFile string
	debug   bool
	output  string
)

var rootCmd = &cobra.Command{
	Use:   "request_getter [request_id]",
	Short: "Get information about an OBS request",
	Args:  cobra.ExactArgs(1),
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

	viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	viper.BindPFlag("api", rootCmd.PersistentFlags().Lookup("api"))
	viper.BindPFlag("output", rootCmd.Flags().Lookup("output"))
}

// Copyright 2018 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/alecthomas/kingpin/v2"
	gomib "github.com/golangsnmp/gomib"
	"github.com/golangsnmp/gomib/mib"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"go.yaml.in/yaml/v2"

	"github.com/prometheus/snmp_exporter/config"
)

// Generate a snmp_exporter config and write it out.
func generateConfig(m *mib.Mib, logger *slog.Logger) error {
	outputPath, err := filepath.Abs(*outputPath)
	if err != nil {
		return fmt.Errorf("unable to determine absolute path for output")
	}

	content, err := os.ReadFile(*generatorYmlPath)
	if err != nil {
		return fmt.Errorf("error reading yml config: %w", err)
	}
	cfg := &Config{}
	err = yaml.UnmarshalStrict(content, cfg)
	if err != nil {
		return fmt.Errorf("error parsing yml config: %w", err)
	}

	outputConfig := config.Config{}
	outputConfig.Auths = cfg.Auths
	outputConfig.Modules = make(map[string]*config.Module, len(cfg.Modules))
	for name, mod := range cfg.Modules {
		logger.Info("Generating config for module", "module", name)
		out, err := generateConfigModule(mod, m, logger)
		if err != nil {
			return err
		}
		outputConfig.Modules[name] = out
		outputConfig.Modules[name].WalkParams = mod.WalkParams
		logger.Info("Generated metrics", "module", name, "metrics", len(outputConfig.Modules[name].Metrics))
	}

	config.DoNotHideSecrets = true
	out, err := yaml.Marshal(outputConfig)
	config.DoNotHideSecrets = false
	if err != nil {
		return fmt.Errorf("error marshaling yml: %w", err)
	}

	// Check the generated config to catch auth/version issues.
	err = yaml.UnmarshalStrict(out, &config.Config{})
	if err != nil {
		return fmt.Errorf("error parsing generated config: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("error opening output file: %w", err)
	}
	out = append([]byte("# WARNING: This file was auto-generated using snmp_exporter generator, manual changes will be lost.\n"), out...)
	_, err = f.Write(out)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("error writing to output file: %w", err)
	}
	logger.Info("Config written", "file", outputPath)
	return nil
}

// loadMIBs loads all MIBs from the configured directories using gomib.
func loadMIBs(logger *slog.Logger) (*mib.Mib, error) {
	opts := []gomib.LoadOption{
		gomib.WithResolverStrictness(mib.ResolverPermissive),
		gomib.WithDiagnosticConfig(mib.DiagnosticConfig{FailAt: mib.SeverityFatal}),
		gomib.WithLogger(logger),
	}

	var sources []gomib.Source
	for _, dir := range *userMibsDir {
		if dir == "" {
			continue
		}
		src, err := gomib.Dir(dir)
		if err != nil {
			return nil, fmt.Errorf("error opening MIB directory %s: %w", dir, err)
		}
		sources = append(sources, src)
	}
	if len(sources) == 0 {
		logger.Info("No MIB directories specified, using system paths")
		opts = append(opts, gomib.WithSystemPaths())
	} else {
		logger.Info("Loading MIBs", "from", *userMibsDir)
		opts = append(opts, gomib.WithSource(sources...))
	}

	return gomib.Load(context.Background(), opts...)
}

var (
	failOnParseErrors  = kingpin.Flag("fail-on-parse-errors", "Exit with a non-zero status if there are MIB parsing errors").Default("true").Bool()
	generateCommand    = kingpin.Command("generate", "Generate snmp.yml from generator.yml")
	userMibsDir        = kingpin.Flag("mibs-dir", "Paths to mibs directory").Default("").Short('m').Strings()
	generatorYmlPath   = generateCommand.Flag("generator-path", "Path to the input generator.yml file").Default("generator.yml").Short('g').String()
	outputPath         = generateCommand.Flag("output-path", "Path to write the snmp_exporter's config file").Default("snmp.yml").Short('o').String()
	parseErrorsCommand = kingpin.Command("parse_errors", "Debug: Print the parse errors from MIB loading")
	dumpCommand        = kingpin.Command("dump", "Debug: Dump the parsed and resolved MIBs")
)

func main() {
	promslogConfig := &promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, promslogConfig)
	kingpin.HelpFlag.Short('h')
	command := kingpin.Parse()
	logger := promslog.New(promslogConfig)

	m, err := loadMIBs(logger)
	if err != nil {
		logger.Warn("MIB loading produced errors", "err", err)
	}
	if m == nil {
		logger.Error("Failed to load MIBs")
		os.Exit(1)
	}

	// Collect diagnostics.
	diags := m.Diagnostics()
	parseErrors := 0
	for _, d := range diags {
		if d.Severity.AtLeast(mib.SeverityError) {
			parseErrors++
		}
	}
	if parseErrors > 0 {
		logger.Warn("MIB loading reported errors", "errors", parseErrors)
	}

	switch command {
	case generateCommand.FullCommand():
		if *failOnParseErrors && parseErrors > 0 {
			logger.Error("Failing on reported parse error(s)", "help", "Use 'generator parse_errors' command to see errors, --no-fail-on-parse-errors to ignore")
		} else {
			err := generateConfig(m, logger)
			if err != nil {
				logger.Error("Error generating config", "err", err)
				os.Exit(1)
			}
		}
	case parseErrorsCommand.FullCommand():
		if len(diags) > 0 {
			for _, d := range diags {
				fmt.Printf("[%s] %s: %s\n", d.Severity, d.Module, d.Message)
			}
		} else {
			logger.Info("No parse errors")
		}
	case dumpCommand.FullCommand():
		for nd := range m.Nodes() {
			dumpNode(nd)
		}
	}
	if *failOnParseErrors && parseErrors > 0 {
		os.Exit(1)
	}
}

func dumpNode(nd *mib.Node) {
	obj := nd.Object()
	name := nd.Name()
	oidStr := nd.OID().String()

	tc := ""
	hint := ""
	desc := ""
	t := ""
	var indexNames []string
	implied := ""
	enums := map[int]string{}
	fixedSize := 0

	if obj != nil {
		tc = objectTCName(obj)
		hint = obj.EffectiveDisplayHint()
		desc = trimDescription(obj.Description())
		if obj.Type() != nil {
			t = obj.Type().EffectiveBase().String()
		}
		fixedSize = objectFixedSize(obj)
		enums = objectEnumValues(obj)

		if obj.Kind() == mib.KindColumn {
			if row := obj.Row(); row != nil {
				indexes := row.EffectiveIndexes()
				for _, idx := range indexes {
					if idx.Object != nil {
						indexNames = append(indexNames, idx.Object.Name())
					} else {
						indexNames = append(indexNames, idx.TypeName)
					}
				}
				if len(indexes) > 0 && indexes[len(indexes)-1].Implied {
					implied = "(implied)"
				}
			}
		}
	} else {
		desc = nd.Description()
	}

	typeStr := t
	if fixedSize != 0 {
		typeStr = fmt.Sprintf("%s(%d)", t, fixedSize)
	}
	fmt.Printf("%s %s %s %q %q %v%s %v %s\n",
		oidStr, name, typeStr, tc, hint, indexNames, implied, enums, desc)
}

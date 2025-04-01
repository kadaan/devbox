// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package boxcli

import (
	"bytes"
	"fmt"
	"github.com/jedib0t/go-pretty/v6/text"
	"io"
	"math"
	"net/url"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"

	"go.jetpack.io/devbox/internal/boxcli/usererr"
	"go.jetpack.io/devbox/internal/searcher"
	"go.jetpack.io/devbox/internal/ux"
)

const trimmedVersionsLength = 10

type searchCmdFlags struct {
	showAll bool
}

func searchCmd() *cobra.Command {
	flags := &searchCmdFlags{}
	command := &cobra.Command{
		Use:   "search <pkg>",
		Short: "Search for nix packages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]
			name, version, isVersioned := searcher.ParseVersionedPackage(query)
			if !isVersioned {
				results, err := searcher.Client().Search(cmd.Context(), query)
				if err != nil {
					return err
				}
				return printSearchResults(
					cmd.OutOrStdout(), query, results, flags.showAll)
			}
			packageVersion, err := searcher.Client().Resolve(name, version)
			if err != nil {
				// This is not ideal. Search service should return valid response we
				// can parse
				return usererr.WithUserMessage(err, "No results found for %q\n", query)
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s resolves to: %s@%s\n",
				query,
				packageVersion.Name,
				packageVersion.Version,
			)
			return nil
		},
	}

	command.Flags().BoolVar(
		&flags.showAll, "show-all", false,
		"show all available templates",
	)

	return command
}

func printSearchResults(
	w io.Writer,
	query string,
	results *searcher.SearchResults,
	showAll bool,
) error {
	if len(results.Packages) == 0 {
		fmt.Fprintf(w, "No results found for %q\n", query)
		return nil
	}
	fmt.Fprintf(
		w,
		"Found %d+ results for %q:\n\n",
		results.NumResults,
		query,
	)

	resultsAreTrimmed := false
	pkgs := results.Packages
	if !showAll && len(pkgs) > trimmedVersionsLength {
		resultsAreTrimmed = true
		pkgs = results.Packages[:int(math.Min(10, float64(len(results.Packages))))]
	}

	rowConfigAutoMerge := table.RowConfig{AutoMerge: true}

	tableWriter := table.NewWriter()
	tableWriter.AppendHeader(table.Row{"Package", "Versions", "Platforms"}, rowConfigAutoMerge)
	for _, pkg := range pkgs {
		systemKey := ""
		var versions []string
		var systems []string
		for i, pkgVersion := range pkg.Versions {
			if pkgVersion.Version == "" {
				continue
			}
			if !showAll && i >= 10 {
				resultsAreTrimmed = true
				break
			}

			var currentSystems []string
			for _, sys := range pkgVersion.Systems {
				currentSystems = append(currentSystems, sys.System)
			}
			slices.Sort(currentSystems)
			key := strings.Join(currentSystems, " ")
			if systemKey != key && systemKey != "" {
				versionStr := columnize(versions, 2)
				versionLines := strings.Count(versionStr, "\n")
				systemColumns := 1
				if versionLines < len(systems) {
					systemColumns = 2
				}
				tableWriter.AppendRow(table.Row{pkg.Name, columnize(versions, 2), columnize(systems, systemColumns)}, rowConfigAutoMerge)
				versions = nil
			}
			systemKey = key
			versions = append(versions, pkgVersion.Version)
			systems = currentSystems
		}

		if len(versions) > 0 {
			versionStr := columnize(versions, 2)
			versionLines := strings.Count(versionStr, "\n")
			systemColumns := 1
			if versionLines < len(systems) {
				systemColumns = 2
			}
			tableWriter.AppendRow(table.Row{pkg.Name, columnize(versions, 2), columnize(systems, systemColumns)}, rowConfigAutoMerge)
		}
	}

	tableWriter.SetColumnConfigs([]table.ColumnConfig{
		{Number: 1, AutoMerge: true, VAlign: text.VAlignMiddle},
		{Number: 2, AutoMerge: true, Align: text.AlignJustify, AlignHeader: text.AlignCenter},
		{Number: 3, AutoMerge: true, Align: text.AlignJustify, AlignHeader: text.AlignCenter},
	})
	tableWriter.SetStyle(table.StyleLight)
	tableWriter.Style().Options.SeparateRows = true
	fmt.Println(tableWriter.Render())

	if resultsAreTrimmed {
		fmt.Println()
		ux.Fwarningf(
			w,
			"Showing top 10 results and truncated versions. Use --show-all to "+
				"show all.\n\n",
		)
	}
	fmt.Printf("For more information go to: https://www.nixhub.io/search?q=%s\n\n", url.QueryEscape(query))

	return nil
}

func columnize(data []string, maxColumns int) string {
	columns := maxColumns
	if len(data) <= columns {
		columns = 1
	}

	buf := bytes.NewBufferString("")
	var versionsGroup []string
	writer := tabwriter.NewWriter(buf, 0, 8, 1, '\t', tabwriter.AlignRight)
	for _, version := range data {
		if len(versionsGroup) == columns {
			_, _ = fmt.Fprintf(writer, "%s\n", strings.Join(versionsGroup, "\t"))
			versionsGroup = nil
		}
		versionsGroup = append(versionsGroup, version)
	}
	if len(versionsGroup) > 0 {
		_, _ = fmt.Fprintf(writer, "%s\n", strings.Join(versionsGroup, "\t"))
	}
	_ = writer.Flush()
	return buf.String()
}

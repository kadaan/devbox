// Copyright 2023 Jetpack Technologies Inc and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package boxcli

import (
	"bytes"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"math"
	"net/url"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	"go.jetpack.io/devbox/internal/boxcli/usererr"
	"go.jetpack.io/devbox/internal/searcher"
	"go.jetpack.io/devbox/internal/ux"
)

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
				results, err := searcher.Client().Search(query)
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
	if !showAll && len(pkgs) > 10 {
		resultsAreTrimmed = true
		pkgs = results.Packages[:int(math.Min(10, float64(len(results.Packages))))]
	}

	rowConfigAutoMerge := table.RowConfig{AutoMerge: true}

	t := table.NewWriter()
	t.AppendHeader(table.Row{"Package", "Versions", "Platforms"}, rowConfigAutoMerge)
	for _, pkg := range pkgs {
		systemKey := ""
		var versions []string
		for i, v := range pkg.Versions {
			if v.Version != "" {
				if !showAll && i >= 10 {
					resultsAreTrimmed = true
					break
				}

				var systems []string
				for _, sys := range v.Systems {
					systems = append(systems, sys.System)
				}
				slices.Sort(systems)
				key := strings.Join(systems, " ")
				if systemKey != key && systemKey != "" {
					t.AppendRow(table.Row{pkg.Name, columnize(versions, 2), systemKey}, rowConfigAutoMerge)
					versions = nil
				}
				systemKey = key
				versions = append(versions, v.Version)
			}
		}

		if len(versions) > 0 {
			t.AppendRow(table.Row{pkg.Name, columnize(versions, 2), systemKey}, rowConfigAutoMerge)
		}
	}

	t.SetColumnConfigs([]table.ColumnConfig{
		{Number: 1, AutoMerge: true, VAlign: text.VAlignMiddle},
		{Number: 2, AutoMerge: true, Align: text.AlignJustify, AlignHeader: text.AlignCenter},
		{Number: 3, AutoMerge: true, Align: text.AlignJustify, AlignHeader: text.AlignCenter, WidthMaxEnforcer: text.WrapSoft, WidthMin: 15, WidthMax: 15},
	})
	t.SetStyle(table.StyleLight)
	t.Style().Options.SeparateRows = true
	fmt.Println(t.Render())

	if resultsAreTrimmed {
		fmt.Println()
		ux.Fwarning(
			w,
			"Showing top 10 results and truncated versions. Use --show-all to "+
				"show all.\n\n",
		)
	}
	ux.Finfo(w, "For more information go to: https://www.nixhub.io/search?q=%s\n\n", url.QueryEscape(query))

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

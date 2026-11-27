// Package dagstore_cmd is `proxi dagstore`: a client of a dagstore's query API (the dagstore is the
// special node keeping the tangle's history, github.com/lunfardo314/dagstore). It talks to the API
// over HTTP only, so proxima does not depend on the dagstore's code.
package dagstore_cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const defaultURL = "http://127.0.0.1:8090"

func Init() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dagstore [<subcommand>]",
		Short: "queries a dagstore, the node keeping the tangle's history",
		Args:  cobra.NoArgs,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.TryReadInConfig()
		},
	}
	cmd.PersistentFlags().String("dagstore.url", "", "URL of the dagstore's query API (default "+defaultURL+")")
	glb.AssertNoError(viper.BindPFlag("dagstore.url", cmd.PersistentFlags().Lookup("dagstore.url")))
	cmd.AddCommand(statusCmd(), txCmd(), branchesCmd(), missingCmd(), exportCmd())
	cmd.InitDefaultHelpCmd()
	return cmd
}

// baseURL is the profile key dagstore.url, or its flag.
func baseURL() string {
	if u := viper.GetString("dagstore.url"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return defaultURL
}

var client = &http.Client{Timeout: 30 * time.Second}

// get fetches path and decodes the JSON answer into ret; an error answer is fatal, with its message.
func get(path string, query url.Values, ret any) {
	u := baseURL() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	glb.Verbosef("GET %s", u)
	resp, err := client.Get(u)
	glb.AssertNoError(err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	glb.AssertNoError(err)
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			glb.Fatalf("%s: %s", resp.Status, e.Error)
		}
		glb.Fatalf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	glb.AssertNoError(json.Unmarshal(body, ret))
}

type point struct {
	Short       string `json:"short"`
	Slot        uint32 `json:"slot"`
	CanonicalNo int64  `json:"canonical_no"`
	Lag         string `json:"lag"`
}

func (p *point) String() string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%s  slot %d, %s behind", p.Short, p.Slot, p.Lag)
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "where the dagstore stands: its history, sync and gaps",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			var st struct {
				Version        string `json:"version"`
				Mode           string `json:"mode"`
				CurrentSlot    uint32 `json:"current_slot"`
				Starting       *point `json:"starting_branch"`
				Sealed         *point `json:"newest_sealed_canonical"`
				Tip            *point `json:"canonical_tip"`
				FeedersLRB     *point `json:"feeders_lrb"`
				MissingPending *int64 `json:"missing_pending"`
				MissingLost    *int64 `json:"missing_lost"`
				Alarms         []string
				Live           *struct {
					FeedersAlive int `json:"feeders_alive"`
					Feeders      int `json:"feeders"`
					Replicas     []struct {
						Peer  string `json:"peer"`
						Alive bool   `json:"alive"`
					} `json:"replicas"`
				} `json:"live"`
			}
			get("/status", nil, &st)
			glb.Infof("dagstore %s at %s: %s", st.Version, baseURL(), st.Mode)
			glb.Infof("current slot:            %d", st.CurrentSlot)
			glb.Infof("starting branch:         %s", st.Starting)
			glb.Infof("newest sealed canonical: %s", st.Sealed)
			glb.Infof("canonical tip:           %s", st.Tip)
			glb.Infof("feeders' LRB:            %s", st.FeedersLRB)
			if st.Live != nil {
				glb.Infof("feeders alive:           %d of %d, replicas: %d", st.Live.FeedersAlive, st.Live.Feeders, len(st.Live.Replicas))
			}
			if st.MissingPending != nil {
				glb.Infof("missing:                 %d pending, %d lost", *st.MissingPending, *st.MissingLost)
			}
			for _, a := range st.Alarms {
				glb.Infof("ALARM: %s", a)
			}
		},
	}
}

func txCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tx <txid>",
		Short: "a transaction as the dagstore holds it: state, confirming branch and the transaction itself",
		Args:  cobra.ExactArgs(1),
		Run: func(_ *cobra.Command, args []string) {
			var d struct {
				TxID             string `json:"txid"`
				Slot             uint32 `json:"slot"`
				Kind             string `json:"kind"`
				State            string `json:"state"`
				ConfirmingBranch string `json:"confirming_branch"`
				ConfirmingNo     int64  `json:"confirming_canonical_no"`
				Source           string `json:"source"`
				ArchiveSegment   string `json:"archive_segment"`
				Parsed           string `json:"parsed"`
				ParseError       string `json:"parse_error"`
			}
			get("/tx/"+url.PathEscape(args[0]), nil, &d)
			glb.Infof("%s  slot %d, %s, %s", d.TxID, d.Slot, d.Kind, d.State)
			if d.ConfirmingBranch != "" {
				glb.Infof("confirmed by canonical branch %d: %s", d.ConfirmingNo, d.ConfirmingBranch)
			} else if d.State == "linked" {
				glb.Infof("on no canonical cone")
			}
			if d.Source != "" {
				glb.Infof("received by %s", d.Source)
			}
			if d.ArchiveSegment != "" {
				glb.Infof("pruned to archive segment %s", d.ArchiveSegment)
			}
			if d.Parsed != "" {
				glb.Infof("\n%s", d.Parsed)
			}
			if d.ParseError != "" {
				glb.Infof("printing failed: %s", d.ParseError)
			}
		},
	}
}

func branchesCmd() *cobra.Command {
	var from, to uint32
	var canonical string
	cmd := &cobra.Command{
		Use:   "branches --from <slot> --to <slot>",
		Short: "branches of a slot range: canonical number or fork, coverage, sealed",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			q := url.Values{"from_slot": {fmt.Sprint(from)}, "to_slot": {fmt.Sprint(to)}}
			if canonical != "" {
				q.Set("canonical", canonical)
			}
			forEachPage("/branches", q, func(raw json.RawMessage) {
				var b struct {
					Short         string `json:"short"`
					Slot          uint32 `json:"slot"`
					TotalCoverage uint64 `json:"total_coverage"`
					CanonicalNo   int64  `json:"canonical_no"`
					Fork          int64  `json:"fork"`
					Sealed        bool   `json:"sealed"`
					Gaps          int64  `json:"gaps"`
				}
				glb.AssertNoError(json.Unmarshal(raw, &b))
				role := fmt.Sprintf("fork %d", b.Fork)
				if b.CanonicalNo != 0 {
					role = fmt.Sprintf("canonical %d", b.CanonicalNo)
				}
				sealed := "unsealed"
				if b.Sealed {
					sealed = "sealed"
					if b.Gaps > 0 {
						sealed = fmt.Sprintf("sealed with %d gaps", b.Gaps)
					}
				}
				glb.Infof("%-8d %s  %-16s coverage %d, %s", b.Slot, b.Short, role, b.TotalCoverage, sealed)
			})
		},
	}
	cmd.Flags().Uint32Var(&from, "from", 0, "first slot")
	cmd.Flags().Uint32Var(&to, "to", 0, "last slot (a range of at most 1000 slots)")
	cmd.Flags().StringVar(&canonical, "canonical", "", "true: canonical only, false: non-canonical only")
	return cmd
}

func missingCmd() *cobra.Command {
	var state string
	cmd := &cobra.Command{
		Use:   "missing",
		Short: "transactions the dagstore misses: pending a pull, or declared lost",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			q := url.Values{}
			if state != "" {
				q.Set("state", state)
			}
			forEachPage("/missing", q, func(raw json.RawMessage) {
				var m struct {
					Short string `json:"short"`
					Slot  uint32 `json:"slot"`
					State string `json:"state"`
					Tried string `json:"tried"`
				}
				glb.AssertNoError(json.Unmarshal(raw, &m))
				glb.Infof("%-8d %s  %-7s %s", m.Slot, m.Short, m.State, m.Tried)
			})
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "missing (pending) or lost; both if not given")
	return cmd
}

// forEachPage walks a paged list to its end.
func forEachPage(path string, q url.Values, fun func(json.RawMessage)) {
	q.Set("limit", "1000")
	for {
		var p struct {
			Items []json.RawMessage `json:"items"`
			Next  string            `json:"next"`
		}
		get(path, q, &p)
		for _, it := range p.Items {
			fun(it)
		}
		if p.Next == "" {
			return
		}
		q.Set("after", p.Next)
	}
}

func exportCmd() *cobra.Command {
	var from, to uint32
	var out string
	cmd := &cobra.Command{
		Use:   "export --from <slot> --to <slot> --out <file>",
		Short: "raw transactions of a slot range, each a 4-byte big-endian length and its bytes, in time order",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			glb.Assertf(out != "", "--out is required")
			u := fmt.Sprintf("%s/export?from_slot=%d&to_slot=%d", baseURL(), from, to)
			resp, err := (&http.Client{}).Get(u) // no timeout: an export streams for as long as the range takes
			glb.AssertNoError(err)
			defer resp.Body.Close()
			glb.Assertf(resp.StatusCode == http.StatusOK, "export: %s", resp.Status)
			f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			glb.AssertNoError(err)
			n, err := io.Copy(f, resp.Body)
			glb.AssertNoError(err)
			glb.AssertNoError(f.Close())
			glb.Infof("slots %d..%d: %d bytes written to %s", from, to, n, out)
		},
	}
	cmd.Flags().Uint32Var(&from, "from", 0, "first slot")
	cmd.Flags().Uint32Var(&to, "to", 0, "last slot")
	cmd.Flags().StringVar(&out, "out", "", "file to write; must not exist")
	return cmd
}

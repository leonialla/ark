package main

import (
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"ssh-tunnel-daemon/internal/daemon"
)

const maxErrLen = 60

func renderTable(w io.Writer, rows []daemon.TunnelStatus, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPORT\tSSH\tREMOTE\tLISTEN\tSTATE\tCONNS\tSINCE\tLAST ERROR")
	for _, r := range rows {
		errMsg := r.LastError
		if errMsg == "" {
			errMsg = "-"
		} else if rs := []rune(errMsg); len(rs) > maxErrLen {
			errMsg = string(rs[:maxErrLen-1]) + "…"
		}
		state := r.State
		if state == "" {
			state = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			r.Name, strconv.Itoa(r.LocalPort), r.SSH, r.Remote, r.Listen, state, r.Conns, age(r.Since, now), errMsg)
	}
	tw.Flush()
}

func age(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

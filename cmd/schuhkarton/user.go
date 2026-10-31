package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/dgrieser/schuhkarton/internal/auth"
	"github.com/dgrieser/schuhkarton/internal/config"
)

const userUsage = `Aufruf: schuhkarton user [-config DATEI] BEFEHL
  list                    Konten zeigen
  add NAME [admin|user]   Konto anlegen (fragt nach dem Passwort)
  passwd NAME             Passwort setzen
  approve NAME            wartendes Konto freigeben
  role NAME admin|user    Rolle ändern
  disable NAME            Konto sperren
  enable NAME             Konto entsperren
  del NAME                Konto löschen
Das Passwort kommt von der Tastatur oder, ohne Terminal, aus der ersten Zeile der Eingabe.
Eine laufende Galerie übernimmt Änderungen nach wenigen Sekunden.`

// runUser manages accounts from the command line (also the way back in
// when the only admin forgot the password).
func runUser(args []string) error {
	cfgPath := ""
	if len(args) >= 2 && (args[0] == "-config" || args[0] == "--config") {
		cfgPath, args = args[1], args[2:]
	}
	if len(args) == 0 {
		return errors.New(userUsage)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	st, err := auth.Open(auth.PathFor(cfg.Path()))
	if err != nil {
		return err
	}
	cmd, rest := args[0], args[1:]
	need := func(n int) error {
		if len(rest) < n {
			return errors.New(userUsage)
		}
		return nil
	}
	switch cmd {
	case "list", "ls":
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tROLLE\tSTATUS\tANGELEGT\tLETZTE ANMELDUNG")
		for _, u := range st.List() {
			status := "aktiv"
			if u.Pending {
				status = "wartet auf Freigabe"
			} else if u.Disabled {
				status = "gesperrt"
			}
			last := "–"
			if !u.LastLogin.IsZero() {
				last = u.LastLogin.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Name, u.Role, status, u.Created.Local().Format("2006-01-02"), last)
		}
		tw.Flush()
		fmt.Printf("(%s)\n", st.Path())
		return nil
	case "add":
		if err := need(1); err != nil {
			return err
		}
		role := auth.RoleUser
		if len(rest) > 1 && rest[1] == auth.RoleAdmin {
			role = auth.RoleAdmin
		}
		pw, err := readNewPassword()
		if err != nil {
			return err
		}
		if err := st.Create(rest[0], pw, role, false); err != nil {
			return err
		}
		fmt.Printf("Konto %s (%s) angelegt.\n", rest[0], role)
		return nil
	case "passwd", "password":
		if err := need(1); err != nil {
			return err
		}
		if _, ok := st.Get(rest[0]); !ok {
			return auth.ErrNotFound
		}
		pw, err := readNewPassword()
		if err != nil {
			return err
		}
		return done(st.Apply(rest[0], auth.Update{Password: &pw}), "Passwort gesetzt; alle Anmeldungen des Kontos sind beendet.")
	case "approve":
		if err := need(1); err != nil {
			return err
		}
		return done(st.Apply(rest[0], auth.Update{Approve: true}), "Freigegeben.")
	case "role":
		if err := need(2); err != nil {
			return err
		}
		if rest[1] != auth.RoleAdmin && rest[1] != auth.RoleUser {
			return errors.New("Rolle: admin oder user")
		}
		return done(st.Apply(rest[0], auth.Update{Role: &rest[1]}), "Rolle geändert.")
	case "disable", "enable":
		if err := need(1); err != nil {
			return err
		}
		off := cmd == "disable"
		return done(st.Apply(rest[0], auth.Update{Disabled: &off}), map[bool]string{true: "Gesperrt.", false: "Entsperrt."}[off])
	case "del", "delete", "rm":
		if err := need(1); err != nil {
			return err
		}
		return done(st.Delete(rest[0]), "Gelöscht.")
	}
	return errors.New(userUsage)
}

func done(err error, msg string) error {
	if err == nil {
		fmt.Println(msg)
	}
	return err
}

func readNewPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("kein Passwort auf der Eingabe")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Neues Passwort: ")
	a, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Noch einmal: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("die Passwörter sind verschieden")
	}
	return string(a), auth.CheckPassword(string(a))
}

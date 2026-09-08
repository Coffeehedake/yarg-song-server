package main

import (
	"fmt"
	"strings"
)

// launcherCmd makes the Windows download double-clickable.
//
// It does NOT open a browser for you, and that is deliberate: launching a URL
// before the server is listening gives a connection-refused page, and the
// person then believes the download is broken when it is merely one second
// early. Printing the address and letting them click it is slower by two
// seconds and never lies.
//
// `cd /d "%~dp0"` is load-bearing. Double-clicking a .cmd inherits whatever
// working directory Explorer feels like, and without it the server would look
// for songs\ and data\ somewhere else entirely - which the config file's
// relative paths would then also miss, silently.
const launcherCmd = `@echo off
setlocal
cd /d "%~dp0"

if not exist "songs" mkdir "songs"
if not exist "data"  mkdir "data"

echo  YARG song server
echo  ----------------
echo  Songs folder : %CD%\songs
echo  Put song folders or .sng files in there.
echo.
echo  When you see "listening" below, open this in a browser:
echo.
echo      http://localhost:8080/
echo.
echo  Close this window to stop the server.
echo.

"%~dp0yarg-song-server.exe" --songs "songs" --data "data"

echo.
echo  The server has stopped.
pause
`

// launcherSh is the same for macOS, Linux and the Pi.
const launcherSh = `#!/bin/sh
# Start the song server with its songs and data folders beside this script.
set -e
cd "$(dirname "$0")"

mkdir -p songs data

echo " YARG song server"
echo " ----------------"
echo " Songs folder : $(pwd)/songs"
echo " Put song folders or .sng files in there."
echo
echo " When you see \"listening\" below, open this in a browser:"
echo
echo "     http://localhost:8080/"
echo
echo " Press Ctrl-C to stop the server."
echo

exec ./yarg-song-server --songs songs --data data
`

// readme is what ships inside the archive. It answers, in order, the four
// questions somebody actually has: what are these two files, how do I start it,
// how do I get songs into YARG, and what do I change.
//
// It is generated per platform rather than written once and shipped everywhere,
// because "run start-server.cmd" and "run ./start-server.sh" are different
// sentences and a README that hedges between them helps nobody.
func readme(version string, t target) string {
	var b strings.Builder

	p := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	p("YARG song server %s (%s/%s)", version, t.GOOS, t.GOARCH+t.Suffix)
	p("=========================================")
	p("")
	p("A song server for YARG. It holds your song library in one place and hands")
	p("songs out over your home network, so every machine that plays does not need")
	p("its own copy.")
	p("")
	p("This is a FORK of YARG's ecosystem, not an official YARC release, and the")
	p("server itself is an independent program. See LICENSE.")
	p("")
	p("WHAT IS IN THIS FOLDER")
	p("----------------------")
	if t.Windows {
		p("  yarg-song-server.exe    the server. Run this on the machine that holds the songs.")
		p("  yarg-sync.exe           the client. Run this on a machine that PLAYS, to copy")
		p("                          songs down from the server into a local folder.")
		p("  start-server.cmd        double-click this to start the server.")
	} else {
		p("  yarg-song-server        the server. Run this on the machine that holds the songs.")
		p("  yarg-sync               the client. Run this on a machine that PLAYS, to copy")
		p("                          songs down from the server into a local folder.")
		p("  start-server.sh         run this to start the server.")
	}
	p("  yarg-song-server.conf   settings, all commented out. Nothing is required.")
	p("  LICENSE                 GNU LGPL v3 or later.")
	p("")

	p("STARTING THE SERVER")
	p("-------------------")
	if t.Windows {
		p("  1. Put your song folders or .sng files into the  songs  folder beside")
		p("     start-server.cmd. (It is created the first time you run it.)")
		p("  2. Double-click  start-server.cmd")
		p("  3. Open  http://localhost:8080/  in a browser. You should see your library.")
		p("")
		p("  Windows SmartScreen will warn you the first time, because these binaries")
		p("  are not code-signed yet. That warning is accurate: it means nobody has")
		p("  paid a certificate authority to vouch for them, not that they are known")
		p("  to be safe. Check the SHA-256 in SHA256SUMS against your download if you")
		p("  want to be sure you have what was published.")
	} else {
		p("  1. Put your song folders or .sng files into the  songs  folder beside")
		p("     start-server.sh. (It is created the first time you run it.)")
		p("  2. Run  ./start-server.sh")
		p("  3. Open  http://localhost:8080/  in a browser. You should see your library.")
		if t.GOOS == "darwin" {
			p("")
			p("  macOS will refuse to run an unsigned downloaded binary. Right-click the")
			p("  binary, choose Open, and confirm once; or clear the quarantine flag with")
			p("      xattr -d com.apple.quarantine yarg-song-server yarg-sync")
			p("  These are not code-signed yet, and that warning is accurate.")
		}
	}
	p("")
	p("  Other machines on your network reach it at  http://<this-machine>:8080/")
	p("  The page works on a phone.")
	p("")

	p("GETTING SONGS INTO YARG")
	p("-----------------------")
	p("  Two ways, and you only need one.")
	p("")
	p("  A. THE SYNC CLIENT, with unmodified YARG.")
	if t.Windows {
		p("     yarg-sync.exe -server http://<server>:8080 -songs \"C:\\path\\to\\your\\songs\"")
	} else {
		p("     ./yarg-sync -server http://<server>:8080 -songs ~/path/to/your/songs")
	}
	p("     It downloads what you are missing as ordinary .sng files and stops. Point")
	p("     YARG at that folder as usual. Run it again whenever the library changes;")
	p("     it only fetches what is new. Add  -dry-run  to see what it would do.")
	p("")
	p("  B. THE FORK OF YARG ITSELF, which fetches on its own. Set the server URL in")
	p("     Settings and it mirrors the library at startup with no separate step.")
	p("")

	p("SETTINGS")
	p("--------")
	p("  Every setting has a sensible default and nothing is required to start.")
	p("  Open  yarg-song-server.conf  to see them all, with what each one costs.")
	p("  A setting can also be given as a command-line flag, and a flag wins over")
	p("  the file. The server prints, at startup, which config file it read and")
	p("  which features are on - so if a setting appears to do nothing, that is the")
	p("  first place to look.")
	p("")
	p("  Two worth knowing about:")
	p("    browse_ui       the web page. On by default.")
	p("    check_uploads   lets the page accept a dropped file and tell you whether")
	p("                    it would work, keeping nothing. OFF by default.")
	p("")

	p("A WARNING WORTH READING")
	p("-----------------------")
	p("  This server has NO authentication, by design. Anyone who can reach the port")
	p("  can browse and download the whole library. That is fine on a home network.")
	p("  DO NOT expose it to the internet, and do not put it behind a reverse proxy")
	p("  on a public hostname.")
	p("")
	p("  It also does not, and will not, decrypt console packages (CON/mogg), and it")
	p("  ships no copyrighted audio.")
	p("")

	p("IF SOMETHING DOES NOT WORK")
	p("--------------------------")
	p("  The startup output is the first thing to read - it says how many songs were")
	p("  indexed and names anything it could not read. The web page shows the same")
	p("  under \"could not be read\".")
	p("")
	p("  A song YARG refuses is usually a song this server also flags. If the page")
	p("  says a song is fine and YARG will not play it, that is a bug worth")
	p("  reporting, and the reverse is too.")
	p("")
	p("  Version: %s", version)

	return b.String()
}

# Building the client fork

**How to produce a playable build of this fork, what it costs, and the one decision that has to be
made before anything is handed to anybody.**

Measured on ENG-1, 2026-09-08. The server is a Go binary and its build is boring; this document is
entirely about the Unity client.

## Running it

```powershell
$env:YARG_BUILD_OUTPUT = "C:\path\to\out\YARG.exe"   # optional; defaults under %TEMP%
& "C:\Program Files\Unity\Hub\Editor\6000.3.5f2\Editor\Unity.exe" `
    -batchmode -nographics `
    -projectPath "C:\dev\YARG - Open Source Contributions\yarg" `
    -logFile "$env:TEMP\yarg-build.log" `
    -executeMethod YARG.Editor.BuildPlayerHeadless.Run
```

Launch it with `Invoke-CimMethod -ClassName Win32_Process -MethodName Create` rather than
`Start-Process` — see `WORKSPACE.md` for why Unity dies otherwise when started from a bridge call.
Grep the log for lines beginning `BUILD `.

## Can the fork even be built? — measured 2026-09-08

**This matters more than it sounds.** The whole project's next gate is a public beta, and a beta
means somebody who is not Jay running the client. Until 2026-09-08 nobody had ever produced a
playable build of this fork; every measurement had been made either against the *official*
v0.15.0 release (the oracle) or in editor batchmode. "It compiles headless" is not "it ships".

### What upstream's own build needs, and what it does not tell us

Upstream has exactly one build workflow, `build-release-mac.yml`, and it is worth reading before
assuming a Windows build is a solved problem:

- It uses `game-ci/unity-builder@v2.2.0` with `targetPlatform: StandaloneOSX`. **There is no
  Windows workflow at all**, so there is no upstream recipe to copy.
- It pins `unityVersion: 2021.3.21f1`. The project's `ProjectVersion.txt` says **6000.3.5f2**.
  That workflow is stale; do not treat its version as authoritative for anything.
- It installs **Blender 3.4.1** and runs **NuGetForUnity restore** before building.

Measured on ENG-1, because those two prerequisites decide whether a local build is even possible:

| | |
|---|---|
| Blender | **not installed** |
| `.blend` files in `Assets` | **1** |
| `Assets/Packages` (NuGetForUnity) | **16 packages restored** |
| `Library/` | 2.5 GB, already warm |
| Free disk | 1.12 TB |

The single `.blend` is already imported into the warm `Library`, so Blender is a **first-import**
dependency rather than a build dependency. A machine with a cold `Library` would need it; this one
does not. That distinction is the sort of thing that turns into a lost afternoon on a fresh
checkout, so it is written down rather than left to be rediscovered.

### `MakeTestBuild` cannot do it, and was not changed

The fork already has `Assets/Editor/MakeTestBuild.cs` from upstream, with *File → Make Test Build*
and *Make Nightly Build* menu items. It cannot be driven from batchmode: it calls
`BuildPlayerWindow.DefaultBuildMethods.GetBuildPlayerOptions(default)`, which asks the editor
where to put the build, and in batchmode there is nobody to ask.

So `Assets/Editor/BuildPlayerHeadless.cs` is a **second entry point**, not an edit to that one.
Upstream's menu items keep behaving exactly as upstream wrote them.

Two decisions inside it are worth stating:

- **Scenes come from `EditorBuildSettings`, not a hardcoded list.** A scene added to the project
  cannot then be silently missing from a build made this way. It found 6.
- **It exits non-zero when the build fails, and asserts the executable exists.**
  `BuildPipeline.BuildPlayer` returns a report rather than throwing, so a build that produced
  nothing at all still leaves the process exiting 0 unless somebody reads the result — and a
  build system that cannot go red is not a build system. It also prints every error message from
  every build step by name, because a summary count tells you the build is broken without telling
  you where to look.

### The first build, measured

Unity 6000.3.5f2, ENG-1, `StandaloneWindows64`, warm `Library`, **cold shader cache**.

| | |
|---|---|
| Wall time | **34.1 minutes** |
| Output | **526 MB** |
| Scenes | 6 |
| Warnings | 103 |
| Errors | **3** — see below |
| Scripting backend | Mono (not IL2CPP) |

**Nearly all of that time is shader compilation, and it is a first-run cost.** Every pass logged
`Local cache hits 0`; single passes compiled 2,880 variants for 6,142 seconds of CPU across 22
shader-compiler processes. It never saturated the machine — average CPU load was **17%** on 22
logical cores — so the box stayed usable throughout. A build runner with a cold cache should
budget for this; a second build on the same machine should not pay it, and that is worth
measuring rather than assuming.

### "Success" came back with three errors, and that is the interesting part

Unity logged `Build Finished, Result: Success` **and** three errors:

```
BUILD ERROR [Preprocess Player]: RenderTexture.Create failed
BUILD ERROR [Preprocess Player]: RenderTexture.Create failed
BUILD ERROR [Building scene Assets/Scenes/Gameplay.unity]: RenderTexture.Create failed
```

Those are almost certainly `-nographics` artefacts — there is no graphics device in batchmode, so
creating a render texture fails — and the resulting player looks complete. **"Almost certainly" is
the honest strength of that claim**: nobody has launched this build. It is a judgement from the
error text and the output, not a measurement.

What the run did prove is that the reporting was worth writing. `BuildPipeline.BuildPlayer`
returns `Succeeded` with a non-zero error count, so a script that only checked the result would
have called this clean. It now says `PASS WITH 3 ERROR(S)` and names each one with the build step
it came from, because a green that has not been earned is worse than a red.

### What is actually in the binary

Verified rather than assumed — the fork's own code is present in `Assembly-CSharp.dll`
(2.1 MB): `SongServerSync`, `MirroredSongs`, `SongServerTab`, `WithServerBadge`, `BadgeMarkup`.
So this is a build of *our* client, not a stock one that happened to compile.

### The naming problem a beta has to solve first

The player identifies itself as **`companyName: YARC`, `productName: YARG`, `bundleVersion: 0.1.0`**
— upstream's values, because they are upstream's project settings and nothing here changed them.

The source now says clearly that this is a modified fork (`FORK-NOTICE.md`, and a note at the top
of the README). **A distributed binary says nothing of the kind.** Somebody handed a `YARG.exe`
from this fork has no way to tell it apart from the official build, which is bad for them — bugs
we introduce look like upstream's — and unfair to upstream, who would field the reports.

This is a decision rather than a task, and it is Jay's: rename the product, add a suffix, ship a
differently-named executable, or accept it for a closed beta among people who already know.
**It should be settled before anything is handed to anyone**, not after.

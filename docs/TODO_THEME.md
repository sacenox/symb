# Code Review: `33bc9ca` — Derive UI chrome colors from Chroma syntax theme

## Medium: `pickAccent` loop range too small

`internal/highlight/highlight.go:187` — The loop `for tt := chroma.TokenType(0); tt < 2000; tt++` only scans token types 0–1999. Chroma's `Name`, `Literal`, `String`, `Operator`, `Comment`, `Generic`, and `Text` tokens all live at 2000+. This means the accent is picked from keyword tokens only, missing the most colorful parts of most themes.

**Fix**: Use a range up to ~9000 or iterate `chroma.TokenTypeValues()`.

## Low: `pickError` lerp direction contradicts its doc comment

`internal/highlight/highlight.go:210-215` — The comment says "lerps 45% toward fg" but the code does `lerpHex(bg, errorColor, 0.45)`, which lerps from bg toward the error color instead. Result is darker than described, though still usable.

## Low: `ColorWarning` is now identical to `ColorError`

`internal/tui/styles.go:24` — Both map to `palette.Error`. Previously warning was a distinct amber. Comment says intentional, but warnings and errors are now visually indistinguishable.

## Trivial: Doc says 60+ entries required, but `github-dark` has 43

`internal/constants/constants.go:24` — Documentation inconsistency between the stated threshold and the actual entry count.

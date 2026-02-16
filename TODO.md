
# TODO write cancels assistant message

- Session: 017549ac3301e35884ed78aa7063ead8
- Assistant completed work, called the todo message without content to clear and got an error
- session is currupted after resume.
- Expected: Assistant reply after calling todo tool, even if it errored. Session to not be corrupted after resume

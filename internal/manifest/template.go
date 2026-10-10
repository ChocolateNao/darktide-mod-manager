package manifest

// Template is the blank manifest `manifest init` writes: every field present so
// the format is visible, every value empty so nothing is claimed.
//
// It is a literal rather than an encoder output because a starter file is read
// by a person before it is read by the manager, and the comments are the point.
// It is built from Names[0] so the file it names is the file Init writes.
//
// The result must stay valid TOML — Init round-trips what it writes through
// Read, so a template that no longer parses fails the test suite rather than
// the user's first install.
var Template = `# Manifest for a mod folder.
#
# Every field here is optional. A folder with no manifest at all is still
# discovered under its own name, so fill in what matters to you and delete the
# rest. The manager reads the first of ` + Names[0] + `, manifest.toml or
# metadata.toml it finds in this folder.

[mod]
id = ""
name = ""
version = "0.0.0"
author = ""
description = ""
# The game version this mod was built against, if it matters to you.
game_version = ""
# Other mods this one needs, and the ones it cannot be installed alongside.
dependencies = []
conflicts_with = []
tags = []

# Hooks are scripts that run at fixed points in a mod's lifecycle. Uncomment a
# block to add one. ` + "`content`" + ` is the script itself, ` + "`type`" + ` is "lua" or
# "javascript", and ` + "`timeout`" + ` is in seconds.
#
# [[hooks.pre_enable]]
# type = "javascript"
# content = "dmm.log.info('enabled')"
# # Run the script in this folder instead of the mod's own.
# working_dir = ""
# # Environment variables the script may read, as KEY=value.
# env = []
# timeout = 10
`

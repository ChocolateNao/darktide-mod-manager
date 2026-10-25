package manifest

// Template is the blank manifest `manifest init` writes: every field present so
// the format is visible, every value empty.
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
source_url = ""
download_url = ""
update_url = ""
# source_data = { key = "value" }

`

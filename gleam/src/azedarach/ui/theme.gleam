// Theme definitions - Catppuccin Macchiato default

pub type Colors {
  Colors(
    // Base colors
    base: String,
    mantle: String,
    crust: String,
    surface0: String,
    surface1: String,
    surface2: String,
    overlay0: String,
    overlay1: String,
    overlay2: String,
    // Text
    text: String,
    subtext0: String,
    subtext1: String,
    // Accents
    rosewater: String,
    flamingo: String,
    pink: String,
    mauve: String,
    red: String,
    maroon: String,
    peach: String,
    yellow: String,
    green: String,
    teal: String,
    sky: String,
    sapphire: String,
    blue: String,
    lavender: String,
  )
}

// Semantic color mappings
pub type SemanticColors {
  SemanticColors(
    background: String,
    foreground: String,
    cursor: String,
    cursor_inactive: String,
    selection: String,
    border: String,
    border_focused: String,
    // Status
    success: String,
    warning: String,
    error: String,
    info: String,
    // Priorities
    priority_1: String,
    priority_2: String,
    priority_3: String,
    priority_4: String,
    // Session states
    session_busy: String,
    session_waiting: String,
    session_done: String,
    session_error: String,
    session_paused: String,
    // Column headers
    column_open: String,
    column_in_progress: String,
    column_blocked: String,
    column_closed: String,
  )
}

pub fn load(theme_name: String) -> Colors {
  case theme_name {
    "catppuccin-macchiato" -> catppuccin_macchiato()
    "catppuccin-mocha" -> catppuccin_mocha()
    "catppuccin-frappe" -> catppuccin_frappe()
    "catppuccin-latte" -> catppuccin_latte()
    _ -> catppuccin_macchiato()
  }
}

pub fn semantic(colors: Colors) -> SemanticColors {
  SemanticColors(
    background: colors.base,
    foreground: colors.text,
    cursor: colors.lavender,
    cursor_inactive: colors.surface2,
    selection: colors.surface1,
    border: colors.surface0,
    border_focused: colors.lavender,
    success: colors.green,
    warning: colors.yellow,
    error: colors.red,
    info: colors.blue,
    priority_1: colors.red,
    priority_2: colors.peach,
    priority_3: colors.yellow,
    priority_4: colors.subtext0,
    session_busy: colors.blue,
    session_waiting: colors.yellow,
    session_done: colors.green,
    session_error: colors.red,
    session_paused: colors.mauve,
    column_open: colors.blue,
    column_in_progress: colors.mauve,
    column_blocked: colors.red,
    column_closed: colors.green,
  )
}

// Catppuccin Macchiato (default)
pub fn catppuccin_macchiato() -> Colors {
  Colors(
    base: "#24273a",
    mantle: "#1e2030",
    crust: "#181926",
    surface0: "#363a4f",
    surface1: "#494d64",
    surface2: "#5b6078",
    overlay0: "#6e738d",
    overlay1: "#8087a2",
    overlay2: "#939ab7",
    text: "#cad3f5",
    subtext0: "#a5adcb",
    subtext1: "#b8c0e0",
    rosewater: "#f4dbd6",
    flamingo: "#f0c6c6",
    pink: "#f5bde6",
    mauve: "#c6a0f6",
    red: "#ed8796",
    maroon: "#ee99a0",
    peach: "#f5a97f",
    yellow: "#eed49f",
    green: "#a6da95",
    teal: "#8bd5ca",
    sky: "#91d7e3",
    sapphire: "#7dc4e4",
    blue: "#8aadf4",
    lavender: "#b7bdf8",
  )
}

// Catppuccin Mocha
pub fn catppuccin_mocha() -> Colors {
  Colors(
    base: "#1e1e2e",
    mantle: "#181825",
    crust: "#11111b",
    surface0: "#313244",
    surface1: "#45475a",
    surface2: "#585b70",
    overlay0: "#6c7086",
    overlay1: "#7f849c",
    overlay2: "#9399b2",
    text: "#cdd6f4",
    subtext0: "#a6adc8",
    subtext1: "#bac2de",
    rosewater: "#f5e0dc",
    flamingo: "#f2cdcd",
    pink: "#f5c2e7",
    mauve: "#cba6f7",
    red: "#f38ba8",
    maroon: "#eba0ac",
    peach: "#fab387",
    yellow: "#f9e2af",
    green: "#a6e3a1",
    teal: "#94e2d5",
    sky: "#89dceb",
    sapphire: "#74c7ec",
    blue: "#89b4fa",
    lavender: "#b4befe",
  )
}

// Catppuccin Frappe
pub fn catppuccin_frappe() -> Colors {
  Colors(
    base: "#303446",
    mantle: "#292c3c",
    crust: "#232634",
    surface0: "#414559",
    surface1: "#51576d",
    surface2: "#626880",
    overlay0: "#737994",
    overlay1: "#838ba7",
    overlay2: "#949cbb",
    text: "#c6d0f5",
    subtext0: "#a5adce",
    subtext1: "#b5bfe2",
    rosewater: "#f2d5cf",
    flamingo: "#eebebe",
    pink: "#f4b8e4",
    mauve: "#ca9ee6",
    red: "#e78284",
    maroon: "#ea999c",
    peach: "#ef9f76",
    yellow: "#e5c890",
    green: "#a6d189",
    teal: "#81c8be",
    sky: "#99d1db",
    sapphire: "#85c1dc",
    blue: "#8caaee",
    lavender: "#babbf1",
  )
}

// Catppuccin Latte (light theme)
pub fn catppuccin_latte() -> Colors {
  Colors(
    base: "#eff1f5",
    mantle: "#e6e9ef",
    crust: "#dce0e8",
    surface0: "#ccd0da",
    surface1: "#bcc0cc",
    surface2: "#acb0be",
    overlay0: "#9ca0b0",
    overlay1: "#8c8fa1",
    overlay2: "#7c7f93",
    text: "#4c4f69",
    subtext0: "#6c6f85",
    subtext1: "#5c5f77",
    rosewater: "#dc8a78",
    flamingo: "#dd7878",
    pink: "#ea76cb",
    mauve: "#8839ef",
    red: "#d20f39",
    maroon: "#e64553",
    peach: "#fe640b",
    yellow: "#df8e1d",
    green: "#40a02b",
    teal: "#179299",
    sky: "#04a5e5",
    sapphire: "#209fb5",
    blue: "#1e66f5",
    lavender: "#7287fd",
  )
}

// Get column header color
pub fn column_color(colors: Colors, column: Int) -> String {
  let sem = semantic(colors)
  case column {
    0 -> sem.column_open
    1 -> sem.column_in_progress
    2 -> sem.column_blocked
    _ -> sem.column_closed
  }
}

// Get priority color
pub fn priority_color(colors: Colors, priority: Int) -> String {
  let sem = semantic(colors)
  case priority {
    1 -> sem.priority_1
    2 -> sem.priority_2
    3 -> sem.priority_3
    _ -> sem.priority_4
  }
}

// Get session state color
pub fn session_color(colors: Colors, state: String) -> String {
  let sem = semantic(colors)
  case state {
    "busy" -> sem.session_busy
    "waiting" -> sem.session_waiting
    "done" -> sem.session_done
    "error" -> sem.session_error
    "paused" -> sem.session_paused
    _ -> colors.subtext0
  }
}

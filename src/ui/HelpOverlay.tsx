/**
 * HelpOverlay component - modal showing all keybindings
 */
import { type Component } from "solid-js"
import { theme } from "./theme"

/**
 * HelpOverlay component
 *
 * Displays a centered modal overlay with all available keybindings
 * grouped by category. Uses Catppuccin theme colors.
 */
export const HelpOverlay: Component = () => {
  const ATTR_BOLD = 1

  return (
    <box
      position="absolute"
      left={0}
      right={0}
      top={0}
      bottom={0}
      alignItems="center"
      justifyContent="center"
      backgroundColor={theme.crust + "CC"} // Semi-transparent overlay
    >
      <box
        borderStyle="rounded"
        border={true}
        borderColor={theme.mauve}
        backgroundColor={theme.base}
        paddingLeft={2}
        paddingRight={2}
        paddingTop={1}
        paddingBottom={1}
        minWidth={60}
      >
        {/* Header */}
        <text fg={theme.mauve} attributes={ATTR_BOLD}>
          {`━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n`}
          {"  KEYBINDINGS HELP\n"}
          {"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"}
          {"\n"}
          {/* Navigation section */}
          <text fg={theme.blue} attributes={ATTR_BOLD}>{"Navigation:\n"}</text>
          <text fg={theme.text}>
            {"  "}
            <text fg={theme.lavender}>{"h j k l"}</text>
            {"        Left / Down / Up / Right\n"}
            {"  "}
            <text fg={theme.lavender}>{"Ctrl-d/u"}</text>
            {"     Half page down / up\n"}
            {"  "}
            <text fg={theme.lavender}>{"gg"}</text>
            {"           Go to first task\n"}
            {"  "}
            <text fg={theme.lavender}>{"ge"}</text>
            {"           Go to last task\n"}
            {"  "}
            <text fg={theme.lavender}>{"gh"}</text>
            {"           Go to first column\n"}
            {"  "}
            <text fg={theme.lavender}>{"gl"}</text>
            {"           Go to last column\n"}
            {"  "}
            <text fg={theme.lavender}>{"gw"}</text>
            {"           Jump mode (shows labels)\n"}
            {"\n"}
            {/* Modes section */}
            <text fg={theme.blue} attributes={ATTR_BOLD}>{"Modes:\n"}</text>
            {"  "}
            <text fg={theme.lavender}>{"v"}</text>
            {"            Enter select mode (multi-select)\n"}
            {"  "}
            <text fg={theme.lavender}>{"Space"}</text>
            {"        Enter action mode (command menu)\n"}
            {"  "}
            <text fg={theme.lavender}>{"g"}</text>
            {"            Enter goto mode (prefix)\n"}
            {"\n"}
            {/* Select mode section */}
            <text fg={theme.blue} attributes={ATTR_BOLD}>{"Select Mode:\n"}</text>
            {"  "}
            <text fg={theme.lavender}>{"Space"}</text>
            {"        Toggle selection of current task\n"}
            {"  "}
            <text fg={theme.lavender}>{"v"}</text>
            {"            Exit select mode\n"}
            {"\n"}
            {/* Action mode section */}
            <text fg={theme.blue} attributes={ATTR_BOLD}>{"Action Mode:\n"}</text>
            {"  "}
            <text fg={theme.lavender}>{"h / l"}</text>
            {"         Move task(s) to previous / next column\n"}
            {"  "}
            <text fg={theme.lavender}>{"s"}</text>
            {"            Start session (coming soon)\n"}
            {"  "}
            <text fg={theme.lavender}>{"a"}</text>
            {"            Attach to session (coming soon)\n"}
            {"  "}
            <text fg={theme.lavender}>{"p"}</text>
            {"            Pause session (coming soon)\n"}
            {"\n"}
            {/* General section */}
            <text fg={theme.blue} attributes={ATTR_BOLD}>{"General:\n"}</text>
            {"  "}
            <text fg={theme.lavender}>{"Enter"}</text>
            {"         Show task details\n"}
            {"  "}
            <text fg={theme.lavender}>{"c"}</text>
            {"            Create new task\n"}
            {"  "}
            <text fg={theme.lavender}>{"q"}</text>
            {"            Quit application\n"}
            {"  "}
            <text fg={theme.lavender}>{"?"}</text>
            {"            Toggle this help screen\n"}
            {"  "}
            <text fg={theme.lavender}>{"Esc"}</text>
            {"          Back to normal mode / dismiss\n"}
            {"\n"}
            <text fg={theme.subtext0}>{"Press any key to dismiss..."}</text>
          </text>
        </text>
      </box>
    </box>
  )
}

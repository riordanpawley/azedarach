import gleeunit/should
import tui_core/color
import etch/style

pub fn from_hex_full_format_test() {
  color.from_hex("#ff0000")
  |> should.equal(Ok(style.Rgb(255, 0, 0)))

  color.from_hex("#00ff00")
  |> should.equal(Ok(style.Rgb(0, 255, 0)))

  color.from_hex("#0000ff")
  |> should.equal(Ok(style.Rgb(0, 0, 255)))

  color.from_hex("#cad3f5")
  |> should.equal(Ok(style.Rgb(202, 211, 245)))
}

pub fn from_hex_shorthand_format_test() {
  // #rgb expands to #rrggbb
  color.from_hex("#f00")
  |> should.equal(Ok(style.Rgb(255, 0, 0)))

  color.from_hex("#0f0")
  |> should.equal(Ok(style.Rgb(0, 255, 0)))

  color.from_hex("#00f")
  |> should.equal(Ok(style.Rgb(0, 0, 255)))

  color.from_hex("#abc")
  |> should.equal(Ok(style.Rgb(170, 187, 204)))
}

pub fn from_hex_without_hash_test() {
  color.from_hex("ff0000")
  |> should.equal(Ok(style.Rgb(255, 0, 0)))

  color.from_hex("abc")
  |> should.equal(Ok(style.Rgb(170, 187, 204)))
}

pub fn from_hex_invalid_test() {
  color.from_hex("#gg0000")
  |> should.equal(Error(Nil))

  color.from_hex("#12345")
  |> should.equal(Error(Nil))

  color.from_hex("")
  |> should.equal(Error(Nil))
}

pub fn parse_hex_test() {
  color.parse_hex("#ff8080")
  |> should.equal(Ok(#(255, 128, 128)))
}

pub fn rgb_test() {
  color.rgb(100, 150, 200)
  |> should.equal(style.Rgb(100, 150, 200))
}

pub fn named_colors_test() {
  // Verify constants are valid etch colors
  color.text |> should.equal(style.Rgb(202, 211, 245))
  color.blue |> should.equal(style.Rgb(138, 173, 244))
  color.red |> should.equal(style.Rgb(237, 135, 150))
  color.base |> should.equal(style.Rgb(36, 39, 58))
}

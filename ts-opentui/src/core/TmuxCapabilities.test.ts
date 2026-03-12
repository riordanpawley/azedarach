import { describe, expect, it } from "bun:test"
import { detectTmuxCapabilities } from "./TmuxCapabilities.js"

describe("detectTmuxCapabilities", () => {
	it("enables tmux actions when TMUX is set", () => {
		const previous = process.env.TMUX
		process.env.TMUX = "/tmp/tmux-501/default,1234,0"
		try {
			expect(detectTmuxCapabilities()).toEqual({
				inTmuxContext: true,
				tmuxActionsEnabled: true,
			})
		} finally {
			process.env.TMUX = previous
		}
	})

	it("disables tmux actions when TMUX is unset or blank", () => {
		const previous = process.env.TMUX
		try {
			delete process.env.TMUX
			expect(detectTmuxCapabilities()).toEqual({
				inTmuxContext: false,
				tmuxActionsEnabled: false,
			})

			process.env.TMUX = "   "
			expect(detectTmuxCapabilities()).toEqual({
				inTmuxContext: false,
				tmuxActionsEnabled: false,
			})
		} finally {
			process.env.TMUX = previous
		}
	})
})

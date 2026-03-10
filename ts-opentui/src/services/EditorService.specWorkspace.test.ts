import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import { EditorService } from "./EditorService.js"

const runEditor = <A>(effect: Effect.Effect<A, unknown, EditorService>) =>
	Effect.runPromise(effect.pipe(Effect.provide(EditorService.Default)))

describe("EditorService spec workspace mode", () => {
	it("enters spec workspace and cycles subviews deterministically", async () => {
		const snapshots = await runEditor(
			Effect.gen(function* () {
				const editor = yield* EditorService
				yield* editor.enterSpecWorkspace()
				const first = yield* editor.getMode()
				yield* editor.cycleSpecSubview()
				const second = yield* editor.getMode()
				yield* editor.cycleSpecSubview()
				const third = yield* editor.getMode()
				yield* editor.cycleSpecSubview()
				const fourth = yield* editor.getMode()
				yield* editor.cycleSpecSubview()
				const fifth = yield* editor.getMode()
				return [first, second, third, fourth, fifth]
			}),
		)

		expect(
			snapshots.map((snapshot) =>
				snapshot._tag === "spec" ? { _tag: snapshot._tag, subview: snapshot.subview } : snapshot,
			),
		).toEqual([
			{ _tag: "spec", subview: "requirements" },
			{ _tag: "spec", subview: "coverage" },
			{ _tag: "spec", subview: "parity" },
			{ _tag: "spec", subview: "publish" },
			{ _tag: "spec", subview: "requirements" },
		])
	})

	it("returns to normal mode on spec workspace exit", async () => {
		const mode = await runEditor(
			Effect.gen(function* () {
				const editor = yield* EditorService
				yield* editor.enterSpecWorkspace()
				yield* editor.exitSpecWorkspace()
				return yield* editor.getMode()
			}),
		)

		expect(mode).toEqual({ _tag: "normal" })
	})

	it("cycles the selected implementation within spec mode after implementations are synced", async () => {
		const snapshots = await runEditor(
			Effect.gen(function* () {
				const editor = yield* EditorService
				yield* editor.enterSpecWorkspace()
				yield* editor.syncSpecImplementations(
					["default", "ts-opentui", "go-bubbletea"],
					"ts-opentui",
				)
				const synced = yield* editor.getMode()
				yield* editor.cycleSpecImplementation("next")
				const next = yield* editor.getMode()
				yield* editor.cycleSpecImplementation("next")
				const wrapped = yield* editor.getMode()
				yield* editor.cycleSpecImplementation("previous")
				const previous = yield* editor.getMode()
				return [synced, next, wrapped, previous]
			}),
		)

		const selected = snapshots.map((snapshot) =>
			snapshot._tag === "spec" ? snapshot.selectedImplementation : null,
		)
		const available = snapshots.map((snapshot) =>
			snapshot._tag === "spec" ? snapshot.availableImplementations : [],
		)

		expect(selected).toEqual(["ts-opentui", "go-bubbletea", "default", "go-bubbletea"])
		expect(available).toEqual([
			["default", "ts-opentui", "go-bubbletea"],
			["default", "ts-opentui", "go-bubbletea"],
			["default", "ts-opentui", "go-bubbletea"],
			["default", "ts-opentui", "go-bubbletea"],
		])
	})
})

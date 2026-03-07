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
                return [first, second, third, fourth]
            }),
        )

        expect(snapshots).toEqual([
            { _tag: "spec", subview: "requirements" },
            { _tag: "spec", subview: "coverage" },
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
})

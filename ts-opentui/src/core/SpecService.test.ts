import { describe, expect, it } from "bun:test"
import { upsertManagedSection } from "./SpecService.js"

describe("upsertManagedSection", () => {
    it("creates a managed section when content is empty", () => {
        const updated = upsertManagedSection("", "OVERVIEW", "# Spec Overview")

        expect(updated).toContain("<!-- AZ-SPEC:OVERVIEW:START -->")
        expect(updated).toContain("# Spec Overview")
        expect(updated).toContain("<!-- AZ-SPEC:OVERVIEW:END -->")
    })

    it("updates existing managed content and preserves unmanaged content", () => {
        const original = [
            "Project notes",
            "",
            "<!-- AZ-SPEC:OVERVIEW:START -->",
            "old content",
            "<!-- AZ-SPEC:OVERVIEW:END -->",
            "",
            "Custom footer",
        ].join("\n")

        const updated = upsertManagedSection(original, "OVERVIEW", "new generated content")

        expect(updated).toContain("Project notes")
        expect(updated).toContain("Custom footer")
        expect(updated).toContain("new generated content")
        expect(updated).not.toContain("old content")
    })

    it("is idempotent for the same rendered payload", () => {
        const once = upsertManagedSection("Custom body", "REQUIREMENTS", "generated")
        const twice = upsertManagedSection(once, "REQUIREMENTS", "generated")

        expect(twice).toBe(once)
    })
})

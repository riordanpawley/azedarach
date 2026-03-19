import { describe, expect, it } from "bun:test"
import { formatLinearOperationError } from "./LinearSdk.js"

describe("formatLinearOperationError", () => {
	it("includes provider validation constraint details when present", () => {
		const message = formatLinearOperationError({
			operation: "issues",
			fallbackError: "Failed to fetch issues from Linear",
			cause: {
				message: "Argument Validation Error",
				raw: {
					response: {
						errors: [
							{
								path: ["issues"],
								extensions: {
									validationErrors: [
										{
											property: "team",
											children: [
												{
													property: "or",
													children: [
														{
															property: 0,
															children: [
																{
																	property: "id",
																	children: [
																		{
																			property: "eq",
																			value: "AZE",
																			constraints: {
																				isUuid: "eq must be a UUID",
																			},
																		},
																	],
																},
															],
														},
													],
												},
											],
										},
									],
								},
							},
						],
					},
				},
			},
		})

		expect(message).toContain("issues: Argument Validation Error")
		expect(message).toContain("issues.team.or.0.id.eq -> isUuid: eq must be a UUID")
		expect(message).toContain('value="AZE"')
	})

	it("falls back to base message when validation details are absent", () => {
		expect(
			formatLinearOperationError({
				operation: "issues",
				fallbackError: "Failed to fetch issues from Linear",
				cause: new Error("Network down"),
			}),
		).toBe("issues: Network down")
	})
})

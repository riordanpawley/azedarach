export const getVisibleSettings = <
	T extends {
		readonly isVisible?: (config: any) => boolean
	},
>(
	settings: readonly T[],
	config: unknown,
): readonly T[] =>
	settings.filter((setting) => (setting.isVisible ? setting.isVisible(config) : true))

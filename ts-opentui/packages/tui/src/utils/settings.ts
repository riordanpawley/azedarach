export const getVisibleSettings = <
	T extends {
		readonly isVisible?: (config: C) => boolean
	},
	C,
>(
	settings: readonly T[],
	config: C,
): readonly T[] =>
	settings.filter((setting) => (setting.isVisible ? setting.isVisible(config) : true))

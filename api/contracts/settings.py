schemas['Appearance'] = obj({'accent_color': {'type':'string','pattern':'^#[0-9a-fA-F]{6}$'}}, ['accent_color'])
route('/settings/appearance','get','getAppearance',ref('Appearance'))
route('/settings/appearance','patch','setAppearance',ref('Appearance'),ref('Appearance'))

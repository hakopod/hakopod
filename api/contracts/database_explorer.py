schemas['DatabaseExplorerConnection'] = obj({'database_id':S,'revision':I,'name':S,'engine':{'type':'string','enum':['postgresql','mysql','mongodb','clickhouse','oracle']},'state':{'type':'string','enum':['eligible','unavailable']},'reason':S,'can_write':B},['database_id','revision','name','engine','state','reason','can_write'])
schemas['DatabaseExplorerCatalog'] = obj({'available':B,'schema_version':{'type':'integer','const':1},'project':S,'environment':S,'items':array(ref('DatabaseExplorerConnection'))},['schema_version','project','environment','items'])
route('/database-explorer/connections','get','listDatabaseExplorerConnections',ref('DatabaseExplorerCatalog'),scope=True)

schemas['DatabaseExplorerHTTP'] = obj({'project':S,'environment':S,'method':S,'path':S,'body':S},['project','environment','method','path','body'])
schemas['DatabaseExplorerHTTPResponse'] = obj({'status':I,'contentType':S,'body':S},['status','contentType','body'])
route('/database-explorer/http','post','proxyDatabaseExplorer',ref('DatabaseExplorerHTTPResponse'),request=ref('DatabaseExplorerHTTP'))

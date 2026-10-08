import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react'
import type * as Monaco from 'monaco-editor'
import { registerYAML, yamlDiagnostics } from '../lib/yaml-language'
import {
  registerSQL,
  sqlDiagnostics,
  supportsSQLGrammar,
  type SQLDialect,
} from '../lib/sql-language'
import { registerTOML, tomlDiagnostics } from '../lib/toml-language'

export type TOMLEditorHandle = { focus: () => void }
export const TOMLEditor = forwardRef<
  TOMLEditorHandle,
  {
    value: string
    onChange: (value: string) => void
    label: string
    describedBy?: string
    onExit?: () => void
    expanded?: boolean
    disabled?: boolean
    readOnly?: boolean
    language?: 'toml' | 'yaml' | 'sql'
    dialect?: SQLDialect
    purpose?: 'compose' | 'workflow'
  }
>(function TOMLEditor(
  {
    value,
    onChange,
    label,
    describedBy,
    onExit,
    expanded,
    disabled,
    readOnly,
    language = 'toml',
    purpose = 'workflow',
    dialect = 'postgresql',
  },
  ref,
) {
  const [loaded, setLoaded] = useState(false)
  const [editorStatus, setEditorStatus] = useState('')
  const container = useRef<HTMLDivElement>(null)
  const editor = useRef<Monaco.editor.IStandaloneCodeEditor | null>(null)
  const latest = useRef({ value, onChange })
  latest.current = { value, onChange }
  const exit = useRef(onExit)
  exit.current = onExit
  useImperativeHandle(ref, () => ({ focus: () => editor.current?.focus() }))
  useEffect(() => {
    let disposed = false
    let cleanup = () => {}
    void Promise.all([
      import('monaco-editor/esm/vs/editor/editor.api'),
      import('monaco-editor/esm/vs/editor/editor.worker?worker'),
    ])
      .then(async ([monaco, { default: EditorWorker }]) => {
        if (disposed || !container.current) return
        self.MonacoEnvironment = { getWorker: () => new EditorWorker() }
        if (language === 'sql') {
          const [{ language: sqlLanguage, conf: sqlConfiguration }] = await Promise.all([
            import('monaco-editor/esm/vs/basic-languages/sql/sql'),
            import('monaco-editor/esm/vs/editor/contrib/suggest/browser/suggestController'),
            import('monaco-editor/esm/vs/editor/contrib/hover/browser/hoverContribution'),
            import('monaco-editor/esm/vs/editor/contrib/gotoError/browser/gotoError'),
          ])
          if (disposed || !container.current) return
          registerSQL(monaco)
          monaco.languages.setMonarchTokensProvider('hakopod-sql', sqlLanguage)
          monaco.languages.setLanguageConfiguration('hakopod-sql', sqlConfiguration)
        }
        language === 'sql'
          ? registerSQL(monaco)
          : language === 'toml'
            ? registerTOML(monaco)
            : registerYAML(monaco)
        const model = monaco.editor.createModel(
          latest.current.value,
          language === 'sql'
            ? 'hakopod-sql'
            : language === 'toml'
              ? 'hakopod-toml'
              : 'hakopod-yaml',
          monaco.Uri.parse(`inmemory://editor/${purpose}-${crypto.randomUUID()}.${language}`),
        )
        const instance = monaco.editor.create(container.current, {
          model,
          ariaLabel: label,
          automaticLayout: true,
          minimap: { enabled: false },
          scrollBeyondLastLine: false,
          wordWrap: 'on',
          fontSize: 14,
          lineNumbers: 'on',
          tabSize: 2,
          readOnly: disabled || readOnly,
          fixedOverflowWidgets: true,
          padding: { top: 12, bottom: 12 },
          accessibilitySupport: 'auto',
        })
        if (language === 'sql')
          instance.addCommand(
            monaco.KeyCode.Escape,
            () => exit.current ? exit.current() : container.current?.parentElement?.focus(),
            '!suggestWidgetVisible',
          )
        editor.current = instance
        setLoaded(true)
        let timer: ReturnType<typeof setTimeout>
        const lint = () =>
          monaco.editor.setModelMarkers(
            model,
            'toml',
            (language === 'sql'
              ? sqlDiagnostics(model.getValue(), dialect)
              : language === 'toml'
                ? tomlDiagnostics(model.getValue())
                : yamlDiagnostics(model.getValue()).map((issue) => ({
                    ...issue,
                    column: issue.col,
                  }))
            ).map((issue) => ({
              severity:
                language === 'sql' ? monaco.MarkerSeverity.Warning : monaco.MarkerSeverity.Error,
              message: issue.message,
              startLineNumber: issue.line,
              endLineNumber: issue.line,
              startColumn: issue.column,
              endColumn: issue.column + 1,
            })),
          )
        let sqlWorker: Worker | null = null
        type LintRequest = { text: string; version: number }
        let pending: LintRequest | null = null
        let active: LintRequest | null = null
        let workerReady = false
        let workerStarting = false
        let workerFailed = false
        let sqlDeadline: ReturnType<typeof setTimeout>
        const stopWorker = (message: string) => {
          clearTimeout(sqlDeadline)
          sqlWorker?.terminate()
          sqlWorker = null
          workerStarting = false
          workerReady = false
          workerFailed = true
          pending = active = null
          if (!disposed && model.getValue().trim()) setEditorStatus(message)
        }
        const sendPending = () => {
          if (disposed || !sqlWorker || !workerReady || active || !pending) return
          active = pending
          pending = null
          try {
            sqlWorker.postMessage({ text: active.text, dialect })
          } catch {
            stopWorker('Grammar lint is unavailable. Your SQL is preserved.')
            return
          }
          sqlDeadline = setTimeout(() => stopWorker('Grammar lint timed out. Your SQL is preserved.'), 2000)
        }
        const grammarLint = async () => {
          monaco.editor.setModelMarkers(model, 'sql-grammar', [])
          const text = model.getValue()
          if (!text.trim()) {
            pending = null
            setEditorStatus('')
            return
          }
          if (language !== 'sql' || !supportsSQLGrammar(dialect) || new TextEncoder().encode(text).length > 65536) return
          pending = { text, version: model.getVersionId() }
          if (workerFailed) return
          if (sqlWorker) { sendPending(); return }
          if (workerStarting) return
          workerStarting = true
          let ParserWorker: (typeof import('../lib/sql-parser.worker?worker'))['default']
          try {
            ;({ default: ParserWorker } = await import('../lib/sql-parser.worker?worker'))
          } catch {
            stopWorker('Grammar lint is unavailable. Your SQL is preserved.')
            return
          }
          if (disposed) return
          let worker: Worker
          try { worker = new ParserWorker() } catch {
            stopWorker('Grammar lint is unavailable. Your SQL is preserved.')
            return
          }
          sqlWorker = worker
          worker.onmessage = (event) => {
            if (disposed || sqlWorker !== worker) return
            if (event.data.ready) {
              clearTimeout(sqlDeadline)
              workerStarting = false
              workerReady = true
              sendPending()
              return
            }
            const version = active?.version
            active = null
            clearTimeout(sqlDeadline)
            if (model.getVersionId() === version && model.getValue().trim()) {
              setEditorStatus(event.data.failed ? 'Grammar lint is unavailable. Your SQL is preserved.' : '')
              monaco.editor.setModelMarkers(
                model,
                'sql-grammar',
                event.data.issues.map(
                  (issue: {
                    line: number
                    column: number
                    endLine: number
                    endColumn: number
                    message: string
                  }) => ({
                    severity: monaco.MarkerSeverity.Warning,
                    message: issue.message,
                    startLineNumber: Math.max(1, Math.min(issue.line, model.getLineCount())),
                    endLineNumber: Math.max(1, Math.min(issue.endLine, model.getLineCount())),
                    startColumn: Math.max(
                      1,
                      Math.min(
                        issue.column,
                        model.getLineMaxColumn(
                          Math.max(1, Math.min(issue.line, model.getLineCount())),
                        ),
                      ),
                    ),
                    endColumn: Math.max(
                      1,
                      Math.min(
                        issue.endColumn,
                        model.getLineMaxColumn(
                          Math.max(1, Math.min(issue.endLine, model.getLineCount())),
                        ),
                      ),
                    ),
                  }),
                ),
              )
            }
            sendPending()
          }
          worker.onerror = () => {
            if (sqlWorker === worker) stopWorker('Grammar lint is unavailable. Your SQL is preserved.')
          }
          sqlDeadline = setTimeout(() => stopWorker('Grammar lint could not start. Your SQL is preserved.'), 10000)
        }
        const subscription = model.onDidChangeContent(() => {
          const next = model.getValue()
          if (language === 'sql') {
            monaco.editor.setModelMarkers(model, 'sql-grammar', [])
            pending = null
            workerFailed = false
            if (!next.trim()) setEditorStatus('')
          }
          if (
            (language === 'sql' ? new TextEncoder().encode(next).length : next.length) >
            (language === 'sql' ? 65536 : 262144)
          ) {
            setEditorStatus('The editor limit was reached. The previous text is preserved.')
            instance.trigger('limit', 'undo', null)
            return
          }
          if (language === 'sql' && next.trim() && supportsSQLGrammar(dialect) && !workerFailed)
            pending = { text: next, version: model.getVersionId() }
          latest.current.onChange(next)
          clearTimeout(timer)
          timer = setTimeout(() => {
            lint()
            void grammarLint()
          }, 300)
        })
        monaco.editor.defineTheme('hakopod-mocha', {
          base: 'vs-dark',
          inherit: true,
          rules: [
            { token: 'comment', foreground: '6C7086', fontStyle: 'italic' },
            { token: 'string', foreground: 'A6E3A1' },
            { token: 'number', foreground: 'FAB387' },
            { token: 'keyword', foreground: 'CBA6F7' },
            { token: 'key', foreground: '89B4FA' },
            { token: 'type', foreground: 'F9E2AF' },
            { token: 'variable', foreground: 'F5C2E7' },
            { token: 'delimiter', foreground: '9399B2' },
          ],
          colors: {
            'editor.background': '#1E1E2E',
            'editor.foreground': '#CDD6F4',
            'editorLineNumber.foreground': '#6C7086',
            'editorLineNumber.activeForeground': '#B4BEFE',
            'editorCursor.foreground': '#F5E0DC',
            'editor.selectionBackground': '#585B7070',
            'editor.inactiveSelectionBackground': '#45475A70',
            'editor.lineHighlightBackground': '#31324460',
            'editorIndentGuide.background1': '#313244',
            'editorIndentGuide.activeBackground1': '#585B70',
            'editorSuggestWidget.background': '#181825',
            'editorSuggestWidget.foreground': '#CDD6F4',
            'editorSuggestWidget.border': '#45475A',
            'editorSuggestWidget.selectedBackground': '#313244',
            'editorHoverWidget.background': '#181825',
            'editorHoverWidget.border': '#45475A',
            'editorError.foreground': '#F38BA8',
            'editorWarning.foreground': '#F9E2AF',
            'scrollbarSlider.background': '#585B7060',
            'scrollbarSlider.hoverBackground': '#6C708680',
          },
        })
        monaco.editor.defineTheme('hakopod-latte', {
          base: 'vs',
          inherit: true,
          rules: [
            { token: 'comment', foreground: '6C6F85', fontStyle: 'italic' },
            { token: 'string', foreground: '40A02B' },
            { token: 'number', foreground: 'FE640B' },
            { token: 'keyword', foreground: '8839EF' },
            { token: 'key', foreground: '1E66F5' },
            { token: 'type', foreground: 'DF8E1D' },
            { token: 'variable', foreground: 'EA76CB' },
            { token: 'delimiter', foreground: '7C7F93' },
          ],
          colors: {
            'editor.background': '#EFF1F5',
            'editor.foreground': '#4C4F69',
            'editorLineNumber.foreground': '#8C8FA1',
            'editorLineNumber.activeForeground': '#7287FD',
            'editorCursor.foreground': '#DC8A78',
            'editor.selectionBackground': '#BCC0CC80',
            'editor.inactiveSelectionBackground': '#CCD0DA80',
            'editor.lineHighlightBackground': '#E6E9EF',
            'editorIndentGuide.background1': '#CCD0DA',
            'editorIndentGuide.activeBackground1': '#9CA0B0',
            'editorSuggestWidget.background': '#E6E9EF',
            'editorSuggestWidget.foreground': '#4C4F69',
            'editorSuggestWidget.border': '#BCC0CC',
            'editorSuggestWidget.selectedBackground': '#CCD0DA',
            'editorHoverWidget.background': '#E6E9EF',
            'editorHoverWidget.border': '#BCC0CC',
            'editorError.foreground': '#D20F39',
            'editorWarning.foreground': '#DF8E1D',
            'scrollbarSlider.background': '#9CA0B060',
            'scrollbarSlider.hoverBackground': '#8C8FA180',
          },
        })
        const theme = () =>
          monaco.editor.setTheme(
            document.documentElement.dataset.theme === 'light' ? 'hakopod-latte' : 'hakopod-mocha',
          )
        const observer = new MutationObserver(theme)
        observer.observe(document.documentElement, {
          attributes: true,
          attributeFilter: ['data-theme'],
        })
        theme()
        lint()
        void grammarLint()
        if (expanded) instance.focus()
        cleanup = () => {
          clearTimeout(timer)
          clearTimeout(sqlDeadline)
          sqlWorker?.terminate()
          observer.disconnect()
          subscription.dispose()
          instance.dispose()
          model.dispose()
          editor.current = null
        }
      })
      .catch(() => {
        if (!disposed) setEditorStatus('The editor could not load. Your text is preserved.')
      })
    return () => {
      disposed = true
      cleanup()
    }
  }, [])
  useEffect(() => {
    const model = editor.current?.getModel()
    if (model && model.getValue() !== value) model.setValue(value)
  }, [value])
  useEffect(() => {
    editor.current?.updateOptions({ readOnly: disabled || readOnly })
  }, [disabled, readOnly])
  return (
    <div
      className={
        expanded
          ? 'flex h-full w-full min-h-0 min-w-0 flex-1 flex-col'
          : 'flex h-[420px] min-w-0 flex-col border border-[var(--border)]'
      }
      role={language === 'sql' ? 'group' : undefined}
      aria-describedby={describedBy}
      aria-label={label}
      tabIndex={language === 'sql' ? 0 : undefined}
    >
      {editorStatus && (
        <p role="status" className="p-2 text-sm">
          {editorStatus}
        </p>
      )}
      <div ref={container} className="min-h-0 w-full flex-1" />
      {!loaded && (
        <pre
          className="min-h-0 flex-1 overflow-auto whitespace-pre-wrap p-3"
          aria-label={`${label} loading`}
        >
          {value}
        </pre>
      )}
    </div>
  )
})

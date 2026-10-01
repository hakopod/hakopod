import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react'
import type * as Monaco from 'monaco-editor'
import { registerYAML, yamlDiagnostics } from '../lib/yaml-language'
import { registerTOML, tomlDiagnostics } from '../lib/toml-language'

export type TOMLEditorHandle = { focus: () => void }
export const TOMLEditor = forwardRef<
  TOMLEditorHandle,
  {
    value: string
    onChange: (value: string) => void
    label: string
    expanded?: boolean
    disabled?: boolean
    readOnly?: boolean
    language?: 'toml' | 'yaml'
    purpose?: 'compose' | 'workflow'
  }
>(function TOMLEditor(
  { value, onChange, label, expanded, disabled, readOnly, language = 'toml', purpose = 'workflow' },
  ref,
) {
  const [loaded, setLoaded] = useState(false)
  const container = useRef<HTMLDivElement>(null)
  const editor = useRef<Monaco.editor.IStandaloneCodeEditor | null>(null)
  const latest = useRef({ value, onChange })
  latest.current = { value, onChange }
  useImperativeHandle(ref, () => ({ focus: () => editor.current?.focus() }))
  useEffect(() => {
    let disposed = false
    let cleanup = () => {}
    void Promise.all([
      import('monaco-editor/esm/vs/editor/editor.api'),
      import('monaco-editor/esm/vs/editor/editor.worker?worker'),
    ]).then(([monaco, { default: EditorWorker }]) => {
      if (disposed || !container.current) return
      self.MonacoEnvironment = { getWorker: () => new EditorWorker() }
      language === 'toml' ? registerTOML(monaco) : registerYAML(monaco)
      const model = monaco.editor.createModel(
        latest.current.value,
        language === 'toml' ? 'hakopod-toml' : 'hakopod-yaml',
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
      editor.current = instance
      setLoaded(true)
      let timer: ReturnType<typeof setTimeout>
      const lint = () =>
        monaco.editor.setModelMarkers(
          model,
          'toml',
          (language === 'toml'
            ? tomlDiagnostics(model.getValue())
            : yamlDiagnostics(model.getValue()).map((issue) => ({ ...issue, column: issue.col }))
          ).map((issue) => ({
            severity: monaco.MarkerSeverity.Error,
            message: issue.message,
            startLineNumber: issue.line,
            endLineNumber: issue.line,
            startColumn: issue.column,
            endColumn: issue.column + 1,
          })),
        )
      const subscription = model.onDidChangeContent(() => {
        const next = model.getValue()
        if (next.length > 262144) {
          instance.trigger('limit', 'undo', null)
          return
        }
        latest.current.onChange(next)
        clearTimeout(timer)
        timer = setTimeout(lint, 300)
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
      if (expanded) instance.focus()
      cleanup = () => {
        clearTimeout(timer)
        observer.disconnect()
        subscription.dispose()
        instance.dispose()
        model.dispose()
        editor.current = null
      }
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
          ? 'h-full w-full min-h-0 min-w-0 flex-1'
          : 'h-[420px] min-w-0 border border-[var(--border)]'
      }
      aria-label={label}
    >
      <div ref={container} className="h-full w-full" />
      {!loaded && <pre className="h-full overflow-auto whitespace-pre-wrap p-3" aria-label={`${label} loading`}>{value}</pre>}
    </div>
  )
})

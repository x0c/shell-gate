import {
  Button,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  EmptyRow,
  FormField,
  InsetList,
  Input,
  LoadingButton,
  Notice,
  Row,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Skeleton,
  toast,
} from '@x0c/ui'
import { Terminal, X } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { errorMessage, postBody, workspaceAPI } from '../lib/api'
import { focusActiveTerminal, isCoarseWorkspace } from '../lib/viewport'
import type { Machine, Preset } from '../types'

const FOLLOW_MACHINE = '__follow__'

export function PresetDialog({
  open,
  onOpenChange,
  presets,
  onPresetsChange,
  machines,
  selectedMachine,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  presets: Preset[]
  onPresetsChange: (presets: Preset[]) => void
  machines: Machine[]
  selectedMachine: string | null
}) {
  const [name, setName] = useState('')
  const [command, setCommand] = useState('')
  const [machine, setMachine] = useState(FOLLOW_MACHINE)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  const [loaded, setLoaded] = useState(false)

  const refresh = async () => {
    const data = await workspaceAPI<{ presets?: Preset[] }>('/api/presets')
    onPresetsChange(data.presets ?? [])
  }

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoaded(false)
    setError('')
    refresh()
      .then(() => { if (!cancelled) setLoaded(true) })
      .catch((cause) => { if (!cancelled) setError(errorMessage(cause, 'Could not load presets')) })
    return () => { cancelled = true }
  }, [open])

  const create = async (event: FormEvent) => {
    event.preventDefault()
    setPending(true)
    setError('')
    try {
      await workspaceAPI('/api/presets', postBody({
        name: name.trim(),
        command: command.trim(),
        machine: machines.length > 1 && machine !== FOLLOW_MACHINE ? machine : '',
        sortOrder: presets.length,
      }))
      setName('')
      setCommand('')
      await refresh()
    } catch (cause) {
      setError(errorMessage(cause, 'Could not add preset'))
    } finally {
      setPending(false)
    }
  }

  const restore = async (preset: Preset) => {
    try {
      await workspaceAPI('/api/presets', postBody({
        name: preset.name,
        command: preset.command,
        machine: preset.machine,
        sortOrder: preset.sortOrder ?? presets.length,
      }))
      await refresh()
    } catch (cause) {
      toast.error(errorMessage(cause, 'Could not restore preset'))
    }
  }

  const remove = async (preset: Preset) => {
    setError('')
    try {
      await workspaceAPI(`/api/presets/${encodeURIComponent(preset.id)}`, { method: 'DELETE' })
      await refresh()
      toast.success('Preset deleted', {
        duration: 5000,
        action: { label: 'Undo', onClick: () => void restore(preset) },
      })
    } catch (cause) {
      setError(errorMessage(cause, 'Could not delete preset'))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="max-w-2xl"
        aria-describedby={undefined}
        onPointerDownOutside={(event) => {
          if ((event.target as Element | null)?.closest('[data-sonner-toaster]')) event.preventDefault()
        }}
        onInteractOutside={(event) => {
          if ((event.target as Element | null)?.closest('[data-sonner-toaster]')) event.preventDefault()
        }}
        onCloseAutoFocus={(event) => {
          if (isCoarseWorkspace()) {
            event.preventDefault()
            focusActiveTerminal()
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>Manage presets</DialogTitle>
        </DialogHeader>
        {error && <Notice tone="error">{error}</Notice>}
        <div className="preset-list">
          {!loaded && presets.length === 0 ? (
            <div className="grid gap-2" aria-busy="true">
              <Skeleton className="h-16 w-full" />
              <Skeleton className="h-16 w-full" />
            </div>
          ) : presets.length ? (
            <InsetList>
              {presets.map((preset) => (
                <Row
                  key={preset.id}
                  className="preset-row"
                  icon={<span className="grid size-8 shrink-0 place-items-center rounded-md bg-muted text-muted-foreground"><Terminal className="size-4" /></span>}
                  title={preset.name}
                  meta={preset.command + (machines.length > 1 && preset.machine ? ` · ${preset.machine}` : '')}
                  actions={
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      title="Delete"
                      aria-label={`Delete ${preset.name}`}
                      onClick={() => void remove(preset)}
                    >
                      <X />
                    </Button>
                  }
                />
              ))}
            </InsetList>
          ) : (
            <InsetList><EmptyRow icon={<Terminal />}>No saved presets</EmptyRow></InsetList>
          )}
        </div>
        <form className="grid gap-4" onSubmit={(event) => void create(event)}>
          <FormField label="Name" htmlFor="presetName">
            <Input id="presetName" value={name} onChange={(event) => setName(event.target.value)} placeholder="e.g. htop" autoComplete="off" required />
          </FormField>
          <FormField label="Command" htmlFor="presetCmd">
            <Input id="presetCmd" value={command} onChange={(event) => setCommand(event.target.value)} placeholder="e.g. htop" autoComplete="off" required />
          </FormField>
          {machines.length > 1 && (
            <FormField label="Machine" htmlFor="presetMachine">
              <Select value={machine} onValueChange={setMachine}>
                <SelectTrigger id="presetMachine">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent position="popper">
                  <SelectItem value={FOLLOW_MACHINE}>{`Follow selection (current: ${selectedMachine || '-'})`}</SelectItem>
                  {machines.map((item) => <SelectItem key={item.name} value={item.name}>{item.name}</SelectItem>)}
                </SelectContent>
              </Select>
            </FormField>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Close</Button>
            <LoadingButton type="submit" pending={pending}>Add</LoadingButton>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

import { useId, type ComponentProps, type ReactNode } from 'react'
import { Button, ButtonAnchor, Form as NoxForm, FormField, ThemeProvider, UIProvider, ValueMenu, useTheme, type ThemePreference } from '@lesomnus/nox'
import type { SignInControls } from '#lib/signin.js'

/** Nox supplies the field surface; native inputs still own values and validation. */
export function Field({ label, id, ...props }: ComponentProps<'input'> & { label: string }): ReactNode {
	const generated = useId()
	const at = id ?? generated
	return <FormField label={label} htmlFor={at}><input {...props} id={at} /></FormField>
}

export function ChoiceField({ label, id, ...props }: ComponentProps<'select'> & { label: string }): ReactNode {
	const generated = useId()
	const at = id ?? generated
	return <FormField label={label} htmlFor={at}><select {...props} id={at} /></FormField>
}

function SignInForm(props: ComponentProps<'form'>): ReactNode {
	return <NoxForm columns={1} {...props} />
}

export const signInControls: SignInControls = { Button, Anchor: ButtonAnchor, Form: SignInForm, Field }

export function AppUI({ children }: { children: ReactNode }): ReactNode {
	return <UIProvider><ThemeProvider defaultPreference="system">{children}</ThemeProvider></UIProvider>
}

export function ThemeChoice(): ReactNode {
	const { preference, setPreference } = useTheme()
	return <ValueMenu label="Appearance" value={preference} variant="compact" options={[
		{ value: 'system', label: 'System' },
		{ value: 'light', label: 'Light' },
		{ value: 'dark', label: 'Dark' },
	]} choose={(value) => setPreference(value as ThemePreference)} />
}

/** Native form behaviour with Nox layout; rows keep their existing submit handlers. */
export function Form(props: ComponentProps<'form'>): ReactNode {
	return <NoxForm columns={1} {...props} />
}

/** Labels remain explicit for search and compact forms that previously used placeholders. */
export function Input(props: ComponentProps<'input'>): ReactNode {
	return <Field label={props['aria-label'] ?? props.placeholder ?? props.name?.replaceAll('_', ' ') ?? 'Value'} {...props} />
}

export function Choice(props: ComponentProps<'select'>): ReactNode {
	return <ChoiceField label={props['aria-label'] ?? props.name?.replaceAll('_', ' ') ?? 'Choose'} {...props} />
}

export function Textarea({ id, ...props }: ComponentProps<'textarea'>): ReactNode {
	const generated = useId()
	const at = id ?? generated
	return <FormField label={props['aria-label'] ?? props.placeholder ?? props.name ?? 'Value'} htmlFor={at}><textarea {...props} id={at} /></FormField>
}

export function RosterMark(): ReactNode {
	return <svg viewBox="0 0 24 24" width="24" height="24" aria-hidden="true"><path d="M3 3h7v7H3zm11 0h7v7h-7zM3 14h7v7H3zm11 0h7v7h-7z" fill="currentColor" /></svg>
}

defmodule HeexApp.SharedLib.Worker do
  def nested_brace_call, do: :ok
  def multiline_call, do: :ok
  def attribute_value_call, do: :ok
  def eex_script_call, do: true
  def eex_style_call, do: true
  def eex_no_curly_call, do: true
  def unicode_call, do: :ok
  def incomplete_call, do: :ok
  def heredoc_tail_call, do: :ok
end

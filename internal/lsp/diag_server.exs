# Dedicated compile-diagnostics BEAM server for Dexter LSP.
#
# Unlike beam_server.exs (which is pure/stateless and shared), this process
# loads a single Mix project into the VM and compiles it, so it is started
# once per project root and kept isolated from the shared BEAM: a compile that
# crashes the VM only takes down diagnostics for that project.
#
# The Go side execs `elixir diag_server.exs <project_root>` with:
#   MIX_ENV=dev
#   MIX_BUILD_ROOT=<project_root>/.dexter/build   (keeps our _build separate
#                                                  from the user's build)
#
# Communication uses the SAME framed binary protocol as beam_server.exs:
#
# Frame 0x00 = request:  request_id(u32) service(u8) op(u8) payload_len(u32) payload
# Frame 0x01 = response: request_id(u32) status(u8) payload_len(u32) payload
# Frame 0x03 = ready:    status(u8) payload_len(u32) payload
#
# Service tag:
#   0x00 = Diag
#
# Diag op 0 (compile) payload:
#   empty (the project root is fixed for the process lifetime)
#
#   Response payload (status 0):
#     count(u32)
#     repeated count times:
#       severity(u8)      0 = error, 1 = warning, 2 = information, 3 = hint
#       start_line(u32) start_col(u32) end_line(u32) end_col(u32)  (1-based; 0 = unknown)
#       file_len(u32) file            (absolute path)
#       message_len(u32) message
#       compiler_len(u16) compiler_name
#
# Force raw byte mode so the Erlang IO server doesn't re-encode our framing.
:io.setopts(:standard_io, encoding: :latin1)

defmodule Dexter.DiagWriter do
  @frame_response 1
  @frame_ready 3

  def send_ready(status, payload \\ <<>>) do
    write(<<@frame_ready::8, status::8, byte_size(payload)::unsigned-big-32, payload::binary>>)
  end

  def send_response(req_id, status, payload) do
    write(
      <<@frame_response::8, req_id::unsigned-big-32, status::8,
        byte_size(payload)::unsigned-big-32, payload::binary>>
    )
  end

  defp write(frame) do
    case IO.binwrite(:stdio, frame) do
      :ok -> :ok
      {:error, reason} -> exit({:write_failed, reason})
    end
  end
end

defmodule Dexter.Diag do
  @severity_error 0
  @severity_warning 1
  @severity_information 2
  @severity_hint 3

  # Runs `mix compile` and returns the full current diagnostic set. Mix.Task.clear/0
  # is required before each rerun in a long-lived VM: without it a second compile
  # of the same project is a :noop and Mix.Task.Compiler.diagnostics/0 keeps
  # returning stale entries.
  #
  # Two diagnostic sources are merged: the rerun's own return value carries the
  # errors from files that FAILED to compile (those never reach the manifest),
  # while Mix.Task.Compiler.diagnostics/0 reads the persisted manifest and so
  # replays warnings for files that were not recompiled this round.
  def compile(mix_exs) do
    try do
      result =
        with_stdout_to_stderr(fn ->
          Mix.Task.clear()
          Mix.Task.rerun("compile", ["--return-errors", "--all-warnings"])
        end)

      returned =
        case result do
          {_status, diags} when is_list(diags) -> diags
          _ -> []
        end

      manifest =
        try do
          Mix.Task.Compiler.diagnostics()
        rescue
          _ -> []
        end

      encode(dedupe(returned ++ manifest))
    rescue
      e -> encode([boot_error_diagnostic(mix_exs, Exception.message(e))])
    catch
      kind, reason -> encode([boot_error_diagnostic(mix_exs, "#{kind}: #{inspect(reason)}")])
    end
  end

  defp dedupe(diagnostics) do
    diagnostics
    |> Enum.uniq_by(fn d ->
      {diag_field(d, :file), diag_field(d, :position), diag_field(d, :message)}
    end)
  end

  # The compiler and Mix tasks write progress ("Compiling N files", "Generated
  # app") to the group leader, which is our stdout binary protocol. Redirect the
  # group leader to stderr for the duration of the compile so nothing corrupts
  # the framing.
  defp with_stdout_to_stderr(fun) do
    old_gl = Process.group_leader()
    Process.group_leader(self(), Process.whereis(:standard_error))

    try do
      fun.()
    after
      Process.group_leader(self(), old_gl)
    end
  end

  defp boot_error_diagnostic(mix_exs, message) do
    %{
      file: mix_exs,
      position: 1,
      span: nil,
      severity: :error,
      message: "dexter: could not compile project: #{message}",
      compiler_name: "dexter"
    }
  end

  defp encode(diagnostics) do
    body =
      for d <- diagnostics, into: <<>> do
        file = to_string(diag_field(d, :file) || "")
        message = to_string(diag_field(d, :message) || "")
        compiler = to_string(diag_field(d, :compiler_name) || "")
        {sl, sc, el, ec} = normalize_position(diag_field(d, :position), diag_field(d, :span))

        <<severity_code(diag_field(d, :severity))::8, sl::unsigned-big-32, sc::unsigned-big-32,
          el::unsigned-big-32, ec::unsigned-big-32, byte_size(file)::unsigned-big-32,
          file::binary, byte_size(message)::unsigned-big-32, message::binary,
          byte_size(compiler)::unsigned-big-16, compiler::binary>>
      end

    {0, <<length(diagnostics)::unsigned-big-32, body::binary>>}
  end

  defp diag_field(%{} = d, key), do: Map.get(d, key)
  defp diag_field(_, _), do: nil

  defp severity_code(:error), do: @severity_error
  defp severity_code(:warning), do: @severity_warning
  defp severity_code(:information), do: @severity_information
  defp severity_code(:hint), do: @severity_hint
  defp severity_code(_), do: @severity_warning

  # Mix positions come in several shapes; normalize every one to a 1-based
  # {start_line, start_col, end_line, end_col} where 0 means "unknown".
  defp normalize_position(nil, _span), do: {0, 0, 0, 0}
  defp normalize_position(line, _span) when is_integer(line), do: {line, 0, line, 0}

  defp normalize_position({line, col}, span) do
    case span do
      {end_line, end_col} -> {line, col, end_line, end_col}
      _ -> {line, col, line, col}
    end
  end

  defp normalize_position({sl, sc, el, ec}, _span), do: {sl, sc, el, ec}
  defp normalize_position(_, _), do: {0, 0, 0, 0}
end

defmodule Dexter.DiagLoop do
  @frame_request 0
  @service_diag 0
  @op_compile 0

  def run(mix_exs) do
    case read_request_frame() do
      {:ok, req_id, @service_diag, @op_compile, _payload} ->
        {status, payload} =
          try do
            Dexter.Diag.compile(mix_exs)
          rescue
            e -> {1, Exception.message(e)}
          catch
            kind, reason -> {1, "#{kind}: #{inspect(reason)}"}
          end

        Dexter.DiagWriter.send_response(req_id, status, payload)
        run(mix_exs)

      {:ok, req_id, service, op, _payload} ->
        Dexter.DiagWriter.send_response(req_id, 1, "unknown request #{service}/#{op}")
        run(mix_exs)

      :eof ->
        :ok

      {:error, reason} ->
        :erlang.display({:diag_loop_read_error, reason})
        :ok
    end
  end

  defp read_request_frame do
    with {:ok, @frame_request} <- read_byte(),
         {:ok, <<req_id::unsigned-big-32>>} <- read_exact(4),
         {:ok, <<service::8, op::8, payload_len::unsigned-big-32>>} <- read_exact(6),
         {:ok, payload} <- read_exact(payload_len) do
      {:ok, req_id, service, op, payload}
    else
      :eof -> :eof
      {:error, :eof} -> :eof
      {:ok, other} -> {:error, {:unexpected_frame, other}}
      {:error, reason} -> {:error, reason}
    end
  end

  defp read_byte do
    case IO.binread(:stdio, 1) do
      :eof -> :eof
      <<byte>> -> {:ok, byte}
      other -> {:error, {:bad_read, other}}
    end
  end

  defp read_exact(0), do: {:ok, <<>>}

  defp read_exact(size) when size > 0 do
    case IO.binread(:stdio, size) do
      :eof -> {:error, :eof}
      data when is_binary(data) and byte_size(data) == size -> {:ok, data}
      data when is_binary(data) -> {:error, {:short_read, size, byte_size(data)}}
      other -> {:error, {:bad_read, other}}
    end
  end
end

# Boot

[project_root_arg] = System.argv()
project_root = Path.expand(project_root_arg)
mix_exs = Path.join(project_root, "mix.exs")

# Defensive: the Go side sets these, but a stray manual launch should still be
# isolated to .dexter/build and the dev environment.
System.put_env("MIX_ENV", "dev")
System.put_env("MIX_BUILD_ROOT", Path.join(project_root, ".dexter/build"))

boot =
  try do
    File.cd!(project_root)
    Mix.start()
    Mix.env(:dev)
    # Suppress Mix's own status output ("Compiling ...", "Generated ...") which
    # would otherwise be written to the stdout binary protocol.
    Mix.shell(Mix.Shell.Quiet)
    # Recompiles in a long-lived VM redefine modules that are already loaded
    # from the previous round; without this every warm compile floods the
    # diagnostics with "redefining module" warnings.
    Code.put_compiler_option(:ignore_module_conflict, true)

    # Compiling mix.exs runs `use Mix.Project`, whose @after_compile hook pushes
    # the project onto the Mix stack — the minimal way to load a project without
    # the mix CLI. loadpaths/deps are handled later by the compile task.
    Code.compile_file(mix_exs)

    case Mix.Project.get() do
      nil ->
        {:error, "no Mix project defined in #{mix_exs}"}

      _module ->
        # The mix CLI loads config/config.exs before any task; libraries like
        # ueberauth read app config at compile time and crash the compile if it
        # is missing. Mirror the CLI here.
        Mix.Task.run("loadconfig")
        :ok
    end
  rescue
    e -> {:error, Exception.message(e)}
  catch
    kind, reason -> {:error, "#{kind}: #{inspect(reason)}"}
  end

case boot do
  :ok ->
    IO.puts(:stderr, "Dexter diag BEAM: started for #{project_root} (pid #{System.pid()})")
    Dexter.DiagWriter.send_ready(0)
    Dexter.DiagLoop.run(mix_exs)

  {:error, message} ->
    IO.puts(:stderr, "Dexter diag BEAM: boot failed: #{message}")
    Dexter.DiagWriter.send_ready(1, to_string(message))
end
